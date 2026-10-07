package collection

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	collectiondb "github.com/thoriqr/stash-it-backend/internal/api/collection/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

type PutSavedItemIntoUserCollectionParams struct {
	UserID      uuid.UUID
	SavedItemID uuid.UUID
	Name        string
}

// PutSavedItemIntoUserCollectionResult reports what the operation did.
//
// CollectionCreated and AlreadyInCollection are mutually exclusive: a collection
// that was just created cannot already hold the saved item.
type PutSavedItemIntoUserCollectionResult struct {
	Collection          collectiondb.Collection
	CollectionCreated   bool
	AlreadyInCollection bool
	SavedItem           SavedItem
}

type Repository interface {
	PutSavedItemIntoUserCollection(
		ctx context.Context,
		params PutSavedItemIntoUserCollectionParams,
	) (PutSavedItemIntoUserCollectionResult, error)
}

type repository struct {
	pool    *pgxpool.Pool
	queries *collectiondb.Queries
}

func NewRepository(
	pool *pgxpool.Pool,
	queries *collectiondb.Queries,
) Repository {
	return &repository{
		pool:    pool,
		queries: queries,
	}
}

// PutSavedItemIntoUserCollection puts one of the user's saved items into the user
// collection with the given name, creating that collection if it does not exist.
//
// The whole operation is a single transaction owned here, because its two halves
// cannot be split across repository calls: a committed collection with no item in
// it would violate the rule that a user collection is only ever created as part of
// filing an item into it. The service therefore calls this one method and does not
// orchestrate anything.
//
// Order matters. The saved item is locked and its ownership verified first, so an
// unknown or foreign saved item fails before any collection row is created, and no
// empty collection can be left behind by that failure. The collection is then
// created or resolved, and the item is moved only if it is not already there.
//
// The ownership check here is application-level. Migration 000025's composite
// foreign key makes the database enforce the same invariant independently, so this
// scoped read is what produces a useful not-found error rather than what keeps
// cross-user filings from happening.
func (r *repository) PutSavedItemIntoUserCollection(
	ctx context.Context,
	params PutSavedItemIntoUserCollectionParams,
) (PutSavedItemIntoUserCollectionResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return PutSavedItemIntoUserCollectionResult{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	// 1. Lock the saved item and verify ownership.
	//
	// Matching id AND user_id means an item that does not exist and an item owned
	// by another user are indistinguishable here, so both produce the same not
	// found error and no collection is created for either.
	locked, err := qtx.LockSavedItemForUser(
		ctx,
		collectiondb.LockSavedItemForUserParams{
			ID:     params.SavedItemID,
			UserID: params.UserID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PutSavedItemIntoUserCollectionResult{}, apperror.NotFoundWith(
				CodeSavedItemNotFound,
				"saved item not found",
				err,
			)
		}

		return PutSavedItemIntoUserCollectionResult{}, apperror.Internal(err)
	}

	// 2. Create the user collection, or resolve the existing one.
	//
	// This is two statements on purpose. CreateUserCollection returns no row when
	// the name is already taken for this user, and the separate lookup that follows
	// takes a fresh snapshot under READ COMMITTED, so it also sees a collection
	// that a concurrent transaction committed while this one waited on the
	// conflicting index entry. A data-modifying CTE cannot be used instead: it
	// shares one snapshot with its own SELECT and would return nothing in that
	// case. A single ON CONFLICT DO UPDATE would return the row too, but
	// collections has an updated_at trigger, so it would write to a pre-existing
	// collection purely to read it.
	collection, collectionCreated, err := createOrGetUserCollection(
		ctx,
		qtx,
		params.UserID,
		params.Name,
	)
	if err != nil {
		return PutSavedItemIntoUserCollectionResult{}, err
	}

	// 3. Move the saved item, unless it is already in the target collection.
	//
	// The operation is idempotent. Skipping the UPDATE entirely is what keeps a
	// repeated call free of side effects: saved_items has an updated_at trigger, so
	// rewriting collection_id with the value it already holds would bump updated_at
	// and make a no-op indistinguishable from a real move.
	savedItem := newSavedItem(locked)

	if locked.CollectionID == collection.ID {
		if err := tx.Commit(ctx); err != nil {
			return PutSavedItemIntoUserCollectionResult{}, apperror.Internal(err)
		}

		return PutSavedItemIntoUserCollectionResult{
			Collection:          collection,
			CollectionCreated:   collectionCreated,
			AlreadyInCollection: true,
			SavedItem:           savedItem,
		}, nil
	}

	moved, err := qtx.MoveSavedItemToCollection(
		ctx,
		collectiondb.MoveSavedItemToCollectionParams{
			CollectionID: collection.ID,
			ID:           params.SavedItemID,
			UserID:       params.UserID,
		},
	)
	if err != nil {
		// The item was locked and owned a moment ago in this same transaction, so
		// no row here means another transaction removed it or changed its owner
		// despite the lock. Both are server side invariant violations.
		if errors.Is(err, pgx.ErrNoRows) {
			return PutSavedItemIntoUserCollectionResult{}, apperror.Internal(
				errors.New("locked saved item disappeared before it could be moved"),
			)
		}

		return PutSavedItemIntoUserCollectionResult{}, apperror.Internal(err)
	}

	// 4. Commit. The collection and the move become visible together, or not at
	// all, so a user collection can never be left holding no item as a result of a
	// failed move.
	if err := tx.Commit(ctx); err != nil {
		return PutSavedItemIntoUserCollectionResult{}, apperror.Internal(err)
	}

	return PutSavedItemIntoUserCollectionResult{
		Collection:        collection,
		CollectionCreated: collectionCreated,
		SavedItem:         newSavedItemFromMove(moved),
	}, nil
}

// createOrGetUserCollection resolves the user's collection with the given name,
// creating it as a user collection when it does not exist yet.
//
// A name already held by a system collection is not a user collection, and
// GetUserCollectionByNameForUser only matches type = 'user'. Returning no row
// therefore means the name is reserved, and that is reported as a conflict instead
// of silently filing the saved item into a system collection the user named by
// accident.
func createOrGetUserCollection(
	ctx context.Context,
	qtx *collectiondb.Queries,
	userID uuid.UUID,
	name string,
) (
	collectiondb.Collection,
	bool,
	error,
) {
	created, err := qtx.CreateUserCollection(
		ctx,
		collectiondb.CreateUserCollectionParams{
			UserID: userID,
			Name:   name,
		},
	)
	if err == nil {
		return created, true, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return collectiondb.Collection{}, false, apperror.Internal(err)
	}

	existing, err := qtx.GetUserCollectionByNameForUser(
		ctx,
		collectiondb.GetUserCollectionByNameForUserParams{
			UserID: userID,
			Name:   name,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return collectiondb.Collection{}, false, apperror.ConflictWith(
				CodeCollectionNameReserved,
				"collection name is already in use",
				nil,
			)
		}

		return collectiondb.Collection{}, false, apperror.Internal(err)
	}

	return existing, false, nil
}

func newSavedItem(
	row collectiondb.LockSavedItemForUserRow,
) SavedItem {
	return SavedItem{
		ID:           row.ID,
		UserID:       row.UserID,
		URL:          row.Url,
		Domain:       row.Domain,
		Platform:     row.Platform,
		Title:        row.Title,
		CollectionID: row.CollectionID,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func newSavedItemFromMove(
	row collectiondb.MoveSavedItemToCollectionRow,
) SavedItem {
	return SavedItem{
		ID:           row.ID,
		UserID:       row.UserID,
		URL:          row.Url,
		Domain:       row.Domain,
		Platform:     row.Platform,
		Title:        row.Title,
		CollectionID: row.CollectionID,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}
