package enrichment

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	enrichmentdb "github.com/thoriqr/stash-it-backend/internal/api/enrichment/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// CompleteSavedItemEnrichmentParams is what a successful enrichment writes.
//
// The four metadata values are passed rather than fetched so that the statement
// stays a single unconditional write: whatever the extractor found, including
// nothing, is what the row ends up holding.
//
// They are pointers because absence is meaningful. A nil field means the page did
// not provide that metadata and the column becomes NULL, which is different from
// storing an empty string. The extractor already models it that way, so the value
// is carried through without being reinterpreted in between.
type CompleteSavedItemEnrichmentParams struct {
	UserID      uuid.UUID
	SavedItemID uuid.UUID
	Title       *string
	Platform    *string
	Description *string
	ImageURL    *string
}

type Repository interface {
	// GetSavedItemURLForEnrichment returns the URL of one of the user's saved
	// items. An item that does not exist and one owned by another user are
	// reported identically, so ownership is never disclosed.
	GetSavedItemURLForEnrichment(
		ctx context.Context,
		userID uuid.UUID,
		savedItemID uuid.UUID,
	) (string, error)

	// CompleteSavedItemEnrichment writes the extracted metadata, marks the item
	// completed and stamps last_enriched_at.
	CompleteSavedItemEnrichment(
		ctx context.Context,
		params CompleteSavedItemEnrichmentParams,
	) (SavedItem, error)

	// FailSavedItemEnrichment records a failed enrichment, touching no metadata
	// column and leaving last_enriched_at alone.
	FailSavedItemEnrichment(
		ctx context.Context,
		userID uuid.UUID,
		savedItemID uuid.UUID,
	) (SavedItem, error)
}

type repository struct {
	queries *enrichmentdb.Queries
}

func NewRepository(
	queries *enrichmentdb.Queries,
) Repository {
	return &repository{
		queries: queries,
	}
}

func (r *repository) GetSavedItemURLForEnrichment(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) (string, error) {
	rawURL, err := r.queries.GetSavedItemURLForEnrichment(
		ctx,
		enrichmentdb.GetSavedItemURLForEnrichmentParams{
			ID:     savedItemID,
			UserID: userID,
		},
	)
	if err != nil {
		// The statement matches on both id and user_id, so a missing item and
		// another user's item are the same result. They surface as one not found
		// error, exactly as the other saved item reads do, so this endpoint never
		// discloses whether an id exists.
		if errors.Is(err, pgx.ErrNoRows) {
			return "", apperror.NotFoundWith(
				CodeSavedItemNotFound,
				"saved item not found",
				err,
			)
		}

		return "", internalError(err)
	}

	return rawURL, nil
}

func (r *repository) CompleteSavedItemEnrichment(
	ctx context.Context,
	params CompleteSavedItemEnrichmentParams,
) (SavedItem, error) {
	row, err := r.queries.CompleteSavedItemEnrichment(
		ctx,
		enrichmentdb.CompleteSavedItemEnrichmentParams{
			Title:       toPersistenceText(params.Title),
			Platform:    toPersistenceText(params.Platform),
			Description: toPersistenceText(params.Description),
			ImageUrl:    toPersistenceText(params.ImageURL),
			ID:          params.SavedItemID,
			UserID:      params.UserID,
		},
	)
	if err != nil {
		// Ownership was already established before this call, so no rows here
		// means the item was deleted in between. That is reported the same way a
		// missing item is, rather than being silently treated as success.
		if errors.Is(err, pgx.ErrNoRows) {
			return SavedItem{}, apperror.NotFoundWith(
				CodeSavedItemNotFound,
				"saved item not found",
				err,
			)
		}

		return SavedItem{}, internalError(err)
	}

	return newSavedItemFromCompleteRow(row), nil
}

func (r *repository) FailSavedItemEnrichment(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) (SavedItem, error) {
	row, err := r.queries.FailSavedItemEnrichment(
		ctx,
		enrichmentdb.FailSavedItemEnrichmentParams{
			ID:     savedItemID,
			UserID: userID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SavedItem{}, apperror.NotFoundWith(
				CodeSavedItemNotFound,
				"saved item not found",
				err,
			)
		}

		return SavedItem{}, internalError(err)
	}

	return newSavedItemFromFailRow(row), nil
}

// newSavedItemFromCompleteRow maps the completed-enrichment row onto the
// feature's projection.
//
// The two update statements project the same columns, but sqlc emits a distinct
// row type per statement, so each is mapped through its own function. That is
// the existing pattern in saved_item and collection rather than a shared type
// invented here.
func newSavedItemFromCompleteRow(
	row enrichmentdb.CompleteSavedItemEnrichmentRow,
) SavedItem {
	return SavedItem{
		ID:               row.ID,
		UserID:           row.UserID,
		Url:              row.Url,
		Domain:           row.Domain,
		Platform:         row.Platform,
		Title:            row.Title,
		Description:      row.Description,
		ImageURL:         row.ImageUrl,
		CollectionID:     row.CollectionID,
		EnrichmentStatus: row.EnrichmentStatus,
		LastEnrichedAt:   row.LastEnrichedAt,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func newSavedItemFromFailRow(
	row enrichmentdb.FailSavedItemEnrichmentRow,
) SavedItem {
	return SavedItem{
		ID:               row.ID,
		UserID:           row.UserID,
		Url:              row.Url,
		Domain:           row.Domain,
		Platform:         row.Platform,
		Title:            row.Title,
		Description:      row.Description,
		ImageURL:         row.ImageUrl,
		CollectionID:     row.CollectionID,
		EnrichmentStatus: row.EnrichmentStatus,
		LastEnrichedAt:   row.LastEnrichedAt,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func internalError(err error) error {
	return apperror.Internal(err)
}
