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
	) (saveditemdb.SavedItem, error)

	GetSavedItemByIDForUser(
		ctx context.Context,
		userID uuid.UUID,
		savedItemID uuid.UUID,
	) (saveditemdb.SavedItem, error)

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
	) ([]saveditemdb.SavedItem, error)

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
) (saveditemdb.SavedItem, error) {
	savedItem, err := r.queries.CreateSavedItem(ctx, params)
	if err != nil {
		return saveditemdb.SavedItem{}, internalError(err)
	}

	return savedItem, nil
}

func (r *repository) GetSavedItemByIDForUser(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) (saveditemdb.SavedItem, error) {
	savedItem, err := r.queries.GetSavedItemByIDForUser(
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
			return saveditemdb.SavedItem{}, apperror.NotFound(err)
		}

		return saveditemdb.SavedItem{}, internalError(err)
	}

	return savedItem, nil
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
) ([]saveditemdb.SavedItem, error) {
	savedItems, err := r.queries.ListSavedItems(
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

	return savedItems, nil
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
