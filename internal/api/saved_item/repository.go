package saved_item

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	saveditemdb "github.com/thoriqr/stash-it-backend/internal/api/saved_item/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

type Repository interface {
	CreateSavedItem(
		ctx context.Context,
		params saveditemdb.CreateSavedItemParams,
	) (SavedItem, error)

	GetUnsortedCollectionByUser(
		ctx context.Context,
		userID uuid.UUID,
	) (uuid.UUID, error)

	GetSavedItemByIDForUser(
		ctx context.Context,
		userID uuid.UUID,
		savedItemID uuid.UUID,
	) (SavedItem, SavedItemCollection, error)

	DeleteSavedItemByIDForUser(
		ctx context.Context,
		userID uuid.UUID,
		savedItemID uuid.UUID,
	) (DeletedSavedItem, error)

	CountSavedItemsInCollection(
		ctx context.Context,
		userID uuid.UUID,
		collectionID uuid.UUID,
	) (int64, error)

	GetCollectionSystemKeyForUser(
		ctx context.Context,
		userID uuid.UUID,
		collectionID uuid.UUID,
	) (pgtype.Text, error)

	// ListSavedItems returns one page of the user's recent saved items across every
	// collection they own, newest first, plus whether more rows follow.
	//
	// The cursor arrives already decoded, validated and parsed, so this never
	// interprets a client-supplied value.
	ListSavedItems(
		ctx context.Context,
		params ListSavedItemsParams,
	) (ListSavedItemsResult, error)
}

type repository struct {
	queries *saveditemdb.Queries
}

func NewRepository(
	queries *saveditemdb.Queries,
) Repository {
	return &repository{
		queries: queries,
	}
}

func (r *repository) CreateSavedItem(
	ctx context.Context,
	params saveditemdb.CreateSavedItemParams,
) (SavedItem, error) {
	// Since migration 000022 added collection_id, saved_items has more columns
	// than these queries project, so sqlc generates a query specific row type
	// instead of reusing the SavedItem model. The row is mapped straight back so
	// the repository interface, the service and the API stay on one type.
	row, err := r.queries.CreateSavedItem(ctx, params)
	if err != nil {
		return SavedItem{}, internalError(err)
	}

	return newSavedItem(
		row.ID,
		row.UserID,
		row.Url,
		row.Domain,
		row.Platform,
		row.Title,
		row.Description,
		row.ImageUrl,
		row.CollectionID,
		row.EnrichmentStatus,
		row.LastEnrichedAt,
		row.CreatedAt,
		row.UpdatedAt,
	), nil
}

func (r *repository) GetUnsortedCollectionByUser(
	ctx context.Context,
	userID uuid.UUID,
) (uuid.UUID, error) {
	collectionID, err := r.queries.GetUnsortedCollectionByUser(ctx, userID)
	if err != nil {
		// Migration 000022 seeds one Unsorted collection per existing user, so a
		// missing row is a broken server side invariant rather than something the
		// caller asked for. It is deliberately not reported as a not found error,
		// and no collection is created here.
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, apperror.Internal(err)
		}

		return uuid.Nil, internalError(err)
	}

	return collectionID, nil
}

// GetSavedItemByIDForUser reads one saved item of the authenticated user together
// with the collection it is filed in.
//
// Both come from one statement. The collection is joined rather than read
// afterwards, so reporting the item's collection costs no second round trip.
//
// The non-disclosure guarantee is unchanged: a missing item and an item owned by
// another user both return no row, so both surface as the same not found error.
func (r *repository) GetSavedItemByIDForUser(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) (SavedItem, SavedItemCollection, error) {
	row, err := r.queries.GetSavedItemByIDForUser(
		ctx,
		saveditemdb.GetSavedItemByIDForUserParams{
			ID:     savedItemID,
			UserID: userID,
		},
	)
	if err != nil {
		// A missing item and an item owned by another user are indistinguishable
		// here, so both surface as the same not found error. That avoids
		// disclosing whether a given ID exists for someone else.
		if errors.Is(err, pgx.ErrNoRows) {
			return SavedItem{}, SavedItemCollection{}, apperror.NotFound(err)
		}

		return SavedItem{}, SavedItemCollection{}, internalError(err)
	}

	return newSavedItem(
		row.ID,
		row.UserID,
		row.Url,
		row.Domain,
		row.Platform,
		row.Title,
		row.Description,
		row.ImageUrl,
		row.CollectionID,
		row.EnrichmentStatus,
		row.LastEnrichedAt,
		row.CreatedAt,
		row.UpdatedAt,
	), SavedItemCollection{
		ID:   row.CollectionID,
		Name: row.CollectionName,
	}, nil
}

func (r *repository) DeleteSavedItemByIDForUser(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) (DeletedSavedItem, error) {
	row, err := r.queries.DeleteSavedItemByIDForUser(
		ctx,
		saveditemdb.DeleteSavedItemByIDForUserParams{
			ID:     savedItemID,
			UserID: userID,
		},
	)
	if err != nil {
		// Same non-disclosure guarantee as GetSavedItemByIDForUser: a missing
		// item and an item owned by another user both return no row, so both
		// surface as the same not found error.
		if errors.Is(err, pgx.ErrNoRows) {
			return DeletedSavedItem{}, apperror.NotFound(err)
		}

		return DeletedSavedItem{}, internalError(err)
	}

	return DeletedSavedItem{
		ID:           row.ID,
		CollectionID: row.CollectionID,
	}, nil
}

// CountSavedItemsInCollection reports how many saved items a collection still
// holds.
//
// The caller runs this after the delete has committed, so the row that was just
// removed is already gone and is never counted. The answer is a fresh
// observation rather than a promise: another request may add or remove an item
// immediately afterwards, and anything that acts on this count re-reads the
// database before committing to a change.
func (r *repository) CountSavedItemsInCollection(
	ctx context.Context,
	userID uuid.UUID,
	collectionID uuid.UUID,
) (int64, error) {
	count, err := r.queries.CountSavedItemsInCollection(
		ctx,
		saveditemdb.CountSavedItemsInCollectionParams{
			CollectionID: collectionID,
			UserID:       userID,
		},
	)
	if err != nil {
		return 0, internalError(err)
	}

	return count, nil
}

// GetCollectionSystemKeyForUser returns the stable identity of one of the user's
// collections.
//
// A collection that does not exist, or belongs to another user, returns an unset
// value rather than an error. That is deliberate and not a swallowed failure:
// the only question this answers is whether the collection is the protected
// Unsorted one, and a collection that is not there is not it. Turning it into an
// error would make an ordinary outcome, a collection another request removed in
// between, read as a server fault.
func (r *repository) GetCollectionSystemKeyForUser(
	ctx context.Context,
	userID uuid.UUID,
	collectionID uuid.UUID,
) (pgtype.Text, error) {
	systemKey, err := r.queries.GetCollectionSystemKeyForUser(
		ctx,
		saveditemdb.GetCollectionSystemKeyForUserParams{
			ID:     collectionID,
			UserID: userID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.Text{}, nil
		}

		return pgtype.Text{}, internalError(err)
	}

	return systemKey, nil
}

// ListSavedItems returns one page of the user's recent saved items.
//
// One row beyond the limit is requested so HasMore is a fact about rows that exist
// rather than an inference from a count. The extra row is dropped before the page is
// returned, and the service builds the next cursor from the last row actually
// returned, never from this lookahead row: the lookahead row was never sent, so a
// cursor built from it would resume past an item the client never received.
//
// No transaction is needed. A listing reads, so there is nothing to keep consistent,
// and the page is a single statement with its own snapshot.
func (r *repository) ListSavedItems(
	ctx context.Context,
	params ListSavedItemsParams,
) (ListSavedItemsResult, error) {
	pageLimit := int32(params.Limit + 1)

	var (
		rows []saveditemdb.ListSavedItemsFirstRow
		err  error
	)

	if params.Cursor == nil {
		rows, err = r.queries.ListSavedItemsFirst(
			ctx,
			saveditemdb.ListSavedItemsFirstParams{
				UserID:    params.UserID,
				PageLimit: pageLimit,
			},
		)
	} else {
		// Both halves of the position were validated and the timestamp parsed by the
		// service before this was called. CursorValue is therefore already the type
		// sqlc generated for a timestamptz column, and a token that survived
		// validation cannot become a query error here.
		afterRow, queryErr := r.queries.ListSavedItemsAfterRow(
			ctx,
			saveditemdb.ListSavedItemsAfterRowParams{
				UserID:      params.UserID,
				CursorValue: params.Cursor.Timestamp,
				CursorID:    *params.Cursor.ID,
				PageLimit:   pageLimit,
			},
		)

		if queryErr != nil {
			return ListSavedItemsResult{}, internalError(queryErr)
		}

		return buildListSavedItemsResult(
			newListedSavedItemsAfterCursor(afterRow),
			params.Limit,
		), nil
	}

	if err != nil {
		return ListSavedItemsResult{}, internalError(err)
	}

	return buildListSavedItemsResult(newListedSavedItems(rows), params.Limit), nil
}

// buildListSavedItemsResult applies the limit and derives HasMore from the lookahead
// row the two statements were asked for.
//
// This is shared rather than restated because the arithmetic has to agree between
// the two statements: a page that reported HasMore from one count and returned rows
// sized by another would either drop an item or send one it never should.
func buildListSavedItemsResult(
	savedItems []ListedSavedItem,
	limit int,
) ListSavedItemsResult {
	hasMore := len(savedItems) > limit
	if hasMore {
		savedItems = savedItems[:limit]
	}

	return ListSavedItemsResult{
		SavedItems: savedItems,
		Limit:      limit,
		HasMore:    hasMore,
	}
}

// newListedSavedItems maps the first-page rows onto the listing projection.
//
// Made with a length rather than left nil, so an empty history serializes as []
// rather than null, which is the shape a caller decoding this list expects.
func newListedSavedItems(
	rows []saveditemdb.ListSavedItemsFirstRow,
) []ListedSavedItem {
	savedItems := make([]ListedSavedItem, 0, len(rows))

	for _, row := range rows {
		savedItems = append(savedItems, ListedSavedItem{
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
			CollectionName:   row.CollectionName,
		})
	}

	return savedItems
}

// newListedSavedItemsAfterCursor maps the resumed-page rows.
//
// sqlc emits a distinct row type per query because the two statements differ, even
// though they project identical columns. This mirrors rather than restates: each row
// field is copied straight across, and both statements are kept column-identical on
// purpose so a future change to one has to be made to the other visibly.
func newListedSavedItemsAfterCursor(
	rows []saveditemdb.ListSavedItemsAfterRowRow,
) []ListedSavedItem {
	savedItems := make([]ListedSavedItem, 0, len(rows))

	for _, row := range rows {
		savedItems = append(savedItems, ListedSavedItem{
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
			CollectionName:   row.CollectionName,
		})
	}

	return savedItems
}

// newSavedItem builds the SavedItem projection from the columns the saved items
// queries project. Since migration 000022 added collection_id, saved_items has
// more columns than these queries select, so sqlc generates a distinct row type
// per query instead of reusing a table model. Mapping the projected columns back
// here keeps the Repository interface, the service and the API on one type.
//
// Every field is taken from the row rather than derived. description and image_url
// are passed through as they were stored, including when they are still NULL, and
// enrichment_status is the column's own value: a create response reporting it is
// reporting what the database wrote, not what this code expected to write.
func newSavedItem(
	id uuid.UUID,
	userID uuid.UUID,
	url string,
	domain pgtype.Text,
	platform pgtype.Text,
	title pgtype.Text,
	description pgtype.Text,
	imageURL pgtype.Text,
	collectionID uuid.UUID,
	enrichmentStatus string,
	lastEnrichedAt pgtype.Timestamptz,
	createdAt pgtype.Timestamptz,
	updatedAt pgtype.Timestamptz,
) SavedItem {
	return SavedItem{
		ID:               id,
		UserID:           userID,
		Url:              url,
		Domain:           domain,
		Platform:         platform,
		Title:            title,
		Description:      description,
		ImageURL:         imageURL,
		CollectionID:     collectionID,
		EnrichmentStatus: enrichmentStatus,
		LastEnrichedAt:   lastEnrichedAt,
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
	}
}

func optionalText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}

	return pgtype.Text{
		String: value,
		Valid:  true,
	}
}
