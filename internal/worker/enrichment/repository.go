package enrichment

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	enrichmentdb "github.com/thoriqr/stash-it-backend/internal/worker/enrichment/generated"
)

// CompleteSavedItemEnrichmentParams is what a successful enrichment writes.
//
// The four metadata values are passed rather than fetched so the statement stays
// a single unconditional write: whatever the extractor found, including nothing,
// is what the row ends up holding. They are pointers because absence is
// meaningful, which is the same reason the API feature passes them as pointers.
type CompleteSavedItemEnrichmentParams struct {
	SavedItemID uuid.UUID
	Title       *string
	Platform    *string
	Description *string
	ImageURL    *string
}

// CompletedSavedItem is what a successful enrichment write reported back.
//
// The statement returns the row's own id and owner rather than having the caller
// re-read them. Two reasons: the caller's earlier load of the same row may already
// be stale, and the value returned by a single autocommit statement is by
// definition committed by the time it is in hand, which is what makes it safe to
// schedule follow-up work from it.
type CompletedSavedItem struct {
	SavedItemID uuid.UUID
	UserID      uuid.UUID

	// Platform is the value that was written, carried here so the caller can decide
	// whether there is anything for organization to act on without re-reading the
	// row. It is the same value the write used, not a re-derivation of it.
	Platform *string
}

// HasPlatform reports whether the enrichment that completed this item produced a
// platform.
//
// A nil platform is the absence of a decision rather than a decision to leave the
// item alone, and it is what a page that exposed nothing, or one whose only
// self-description enrichment deliberately refuses to treat as a platform,
// produces. It is the condition under which automatic organization has nothing to
// work from.
func (c CompletedSavedItem) HasPlatform() bool {
	return c.Platform != nil && *c.Platform != ""
}

// Repository is the worker's own persistence for background enrichment.
//
// It is a separate interface from internal/api/enrichment.Repository on purpose.
// The two run in different binaries against different sqlc targets, and sharing
// one interface would mean sharing generated models and row types across that
// boundary to avoid a few similar methods. The queries themselves are the same
// statements and are documented to match.
type Repository interface {
	// GetSavedItemForBackgroundEnrichment returns the saved item a task is about,
	// or ErrSavedItemNotFound when it no longer exists.
	GetSavedItemForBackgroundEnrichment(
		ctx context.Context,
		savedItemID uuid.UUID,
	) (SavedItem, error)

	// CompleteSavedItemEnrichment writes the extracted metadata, marks the item
	// completed and stamps last_enriched_at, returning the item's own id and owner
	// as the statement reported them.
	//
	// The returned identity is what a caller schedules follow-up work from. Because
	// the write is a single autocommit statement, those values are committed by the
	// time they are returned.
	CompleteSavedItemEnrichment(
		ctx context.Context,
		params CompleteSavedItemEnrichmentParams,
	) (CompletedSavedItem, error)

	// FailSavedItemEnrichment records a failed enrichment, touching no metadata
	// column and leaving last_enriched_at alone.
	FailSavedItemEnrichment(
		ctx context.Context,
		savedItemID uuid.UUID,
	) error
}

type repository struct {
	queries *enrichmentdb.Queries
}

func NewRepository(queries *enrichmentdb.Queries) Repository {
	return &repository{
		queries: queries,
	}
}

func (r *repository) GetSavedItemForBackgroundEnrichment(
	ctx context.Context,
	savedItemID uuid.UUID,
) (SavedItem, error) {
	row, err := r.queries.GetSavedItemForBackgroundEnrichment(
		ctx,
		savedItemID,
	)
	if err != nil {
		// No rows means the item was deleted while the task waited. That is a
		// normal outcome for a queue whose tasks are processed later than they
		// were created, so it is reported as the sentinel the handler discards on
		// rather than as an internal error.
		if errors.Is(err, pgx.ErrNoRows) {
			return SavedItem{}, notFoundError()
		}

		return SavedItem{}, internalError(err)
	}

	return SavedItem{
		ID:     row.ID,
		UserID: row.UserID,
		Url:    row.Url,
	}, nil
}

func (r *repository) CompleteSavedItemEnrichment(
	ctx context.Context,
	params CompleteSavedItemEnrichmentParams,
) (CompletedSavedItem, error) {
	row, err := r.queries.CompleteSavedItemEnrichment(
		ctx,
		enrichmentdb.CompleteSavedItemEnrichmentParams{
			Title:       toPersistenceText(params.Title),
			Platform:    toPersistenceText(params.Platform),
			Description: toPersistenceText(params.Description),
			ImageUrl:    toPersistenceText(params.ImageURL),
			ID:          params.SavedItemID,
		},
	)
	if err != nil {
		// The item was loaded moments ago, so no rows here means it was deleted
		// between the load and the write. That is the same situation the load
		// reports, so it gets the same sentinel rather than being treated as
		// success: silently succeeding would leave a task that did nothing looking
		// like one that did its work.
		if errors.Is(err, pgx.ErrNoRows) {
			return CompletedSavedItem{}, notFoundError()
		}

		return CompletedSavedItem{}, internalError(err)
	}

	// The statement's own result is preferred over the values passed in, because
	// they are what the database confirmed rather than what was asked for.
	return CompletedSavedItem{
		SavedItemID: row.ID,
		UserID:      row.UserID,
		Platform:    params.Platform,
	}, nil
}

func (r *repository) FailSavedItemEnrichment(
	ctx context.Context,
	savedItemID uuid.UUID,
) error {
	_, err := r.queries.FailSavedItemEnrichment(ctx, savedItemID)
	if err != nil {
		// Same reasoning as above, and for the same reason: the sentinel is what
		// tells the handler the item is gone instead of the database having a bad
		// moment.
		if errors.Is(err, pgx.ErrNoRows) {
			return notFoundError()
		}

		return internalError(err)
	}

	return nil
}

// notFoundError builds the one error a missing item produces.
//
// It takes no argument on purpose. All three call sites reach it from the same
// condition, pgx.ErrNoRows, and the driver error behind that condition carries
// no information beyond "no row matched an id that came from a committed save",
// which is already what the sentinel says. Keeping it as the cause would make
// the two indistinguishable to a log reader and would suggest the error is more
// specific than it is.
//
// It carries the code as well as the sentinel because the sentinel alone loses
// the code through AppError.Unwrap for anything that inspects the code. The
// handler matches on ErrSavedItemNotFound; the code is here so a log line and any
// future non-HTTP consumer see the same identifier the synchronous path uses.
func notFoundError() error {
	return apperror.NotFoundWith(
		CodeSavedItemNotFound,
		"saved item not found",
		ErrSavedItemNotFound,
	)
}

func internalError(err error) error {
	return apperror.Internal(err)
}
