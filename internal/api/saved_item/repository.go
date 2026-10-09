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

func optionalText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}

	return pgtype.Text{
		String: value,
		Valid:  true,
	}
}
