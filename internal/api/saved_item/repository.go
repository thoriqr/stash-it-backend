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
	) (SavedItem, error)

	DeleteSavedItemByIDForUser(
		ctx context.Context,
		userID uuid.UUID,
		savedItemID uuid.UUID,
	) error

	ListSavedItems(
		ctx context.Context,
		userID uuid.UUID,
		offset int32,
		limit int32,
	) ([]SavedItem, error)

	CountSavedItems(
		ctx context.Context,
		userID uuid.UUID,
	) (int64, error)
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

func (r *repository) GetSavedItemByIDForUser(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) (SavedItem, error) {
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
			return SavedItem{}, apperror.NotFound(err)
		}

		return SavedItem{}, internalError(err)
	}

	return newSavedItem(
		row.ID,
		row.UserID,
		row.Url,
		row.Domain,
		row.Platform,
		row.Title,
		row.CreatedAt,
		row.UpdatedAt,
	), nil
}

func (r *repository) DeleteSavedItemByIDForUser(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) error {
	_, err := r.queries.DeleteSavedItemByIDForUser(
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
			return apperror.NotFound(err)
		}

		return internalError(err)
	}

	return nil
}

func (r *repository) ListSavedItems(
	ctx context.Context,
	userID uuid.UUID,
	offset int32,
	limit int32,
) ([]SavedItem, error) {
	rows, err := r.queries.ListSavedItems(
		ctx,
		saveditemdb.ListSavedItemsParams{
			UserID:     userID,
			PageOffset: offset,
			PageLimit:  limit,
		},
	)
	if err != nil {
		return nil, internalError(err)
	}

	savedItems := make([]SavedItem, 0, len(rows))
	for _, row := range rows {
		savedItems = append(
			savedItems,
			newSavedItem(
				row.ID,
				row.UserID,
				row.Url,
				row.Domain,
				row.Platform,
				row.Title,
				row.CreatedAt,
				row.UpdatedAt,
			),
		)
	}

	return savedItems, nil
}

// newSavedItem builds the SavedItem projection from the columns the saved items
// queries project. Since migration 000022 added collection_id, saved_items has
// more columns than these queries select, so sqlc generates a distinct row type
// per query instead of reusing a table model. Mapping the projected columns back
// here keeps the Repository interface, the service and the API on one type.
//
// collection_id and the enrichment columns are not projected by these queries, so
// SavedItem has no field for them and nothing can read a zero value by accident.
// The collection feature carries its own projection, which does include
// collection_id.
func newSavedItem(
	id uuid.UUID,
	userID uuid.UUID,
	url string,
	domain pgtype.Text,
	platform pgtype.Text,
	title pgtype.Text,
	createdAt pgtype.Timestamptz,
	updatedAt pgtype.Timestamptz,
) SavedItem {
	return SavedItem{
		ID:        id,
		UserID:    userID,
		Url:       url,
		Domain:    domain,
		Platform:  platform,
		Title:     title,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
}

func (r *repository) CountSavedItems(
	ctx context.Context,
	userID uuid.UUID,
) (int64, error) {
	count, err := r.queries.CountSavedItems(ctx, userID)
	if err != nil {
		return 0, internalError(err)
	}

	return count, nil
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
