package collection

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
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

	// DeleteCollection removes one collection of the user and deals with the saved
	// items in it according to the action the caller chose.
	DeleteCollection(
		ctx context.Context,
		params DeleteCollectionParams,
	) error
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

// DeleteCollection removes one collection of the authenticated user, after
// resolving what happens to the saved items filed in it.
//
// The whole operation is a single transaction owned here, because the halves are
// not separable. A collection and its items cannot both be left alone: the
// collection has to go, and saved_items.collection_id is NOT NULL, so the items
// must be deleted or moved first. Committing the item disposition without the
// collection would leave an empty collection that no operation had finished
// removing, and committing the collection first would fail on the foreign key.
// All-or-nothing is the only honest outcome, and a caller who gets an error must
// be able to rely on nothing having changed.
//
// The sequence:
//
//  1. Load the source collection, scoped to the user. No row means it does not
//     exist or belongs to somebody else, and the two are told apart by nothing: the
//     caller learns only that it cannot delete this collection.
//  2. Refuse to remove Unsorted, identified by system_key.
//  3. Load the target collection when the action is 'move'. This happens before any
//     item is touched, so a target that cannot be used fails while the collection
//     and its items are still untouched.
//  4. Delete or move the collection's saved items.
//  5. Delete the collection, still guarded by the foreign key.
//
// Nothing is taken FOR UPDATE. See the query comments: the move endpoint locks a
// saved item and then touches a collection, so locking the collection first here
// would invert that order. Emptiness is decided by the constraint at step 5 rather
// than by a read, which is both race-free and cheaper.
//
// Ownership is asserted on every statement by user_id, and migration 000025's
// composite foreign key checks the same thing independently at the database.
func (r *repository) DeleteCollection(
	ctx context.Context,
	params DeleteCollectionParams,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	// 1. Load the source collection and prove ownership.
	//
	// Unsorted is refused here, before any item is touched, so the one protected
	// collection cannot have its items disturbed either.
	source, err := qtx.GetCollectionByIDForUser(
		ctx,
		collectiondb.GetCollectionByIDForUserParams{
			ID:     params.CollectionID,
			UserID: params.UserID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperror.NotFoundWith(
				CodeCollectionNotFound,
				"collection not found",
				err,
			)
		}

		return apperror.Internal(err)
	}

	if isUnsorted(source.SystemKey) {
		return apperror.ConflictWith(
			CodeUnsortedCollectionProtected,
			"the unsorted collection cannot be deleted",
			nil,
		)
	}

	// 2. Resolve the target before anything is written, when there is one.
	//
	// Doing this first means a missing or unusable target fails with the
	// collection and its items completely untouched. There is no fallback to any
	// other collection, including Unsorted: the caller named a target, so the
	// backend executes that or reports that it cannot.
	if params.Action == SavedItemsActionMove {
		if _, err := qtx.GetCollectionByIDForUser(
			ctx,
			collectiondb.GetCollectionByIDForUserParams{
				ID:     params.TargetCollectionID,
				UserID: params.UserID,
			},
		); err != nil {
			// A target owned by somebody else returns no row here exactly as an
			// unknown one does, so this error discloses nothing about collections
			// the caller cannot see.
			if errors.Is(err, pgx.ErrNoRows) {
				return apperror.NotFoundWith(
					CodeCollectionDeleteTargetNotFound,
					"target collection not found",
					err,
				)
			}

			return apperror.Internal(err)
		}
	}

	// 3. Dispose of the collection's saved items.
	if params.Action == SavedItemsActionMove {
		if err := qtx.MoveSavedItemsToCollection(
			ctx,
			collectiondb.MoveSavedItemsToCollectionParams{
				TargetCollectionID: params.TargetCollectionID,
				CollectionID:       params.CollectionID,
				UserID:             params.UserID,
			},
		); err != nil {
			return mapCollectionChildrenError(err)
		}
	} else {
		if err := qtx.DeleteSavedItemsInCollection(
			ctx,
			collectiondb.DeleteSavedItemsInCollectionParams{
				CollectionID: params.CollectionID,
				UserID:       params.UserID,
			},
		); err != nil {
			return mapCollectionChildrenError(err)
		}
	}

	// 4. Delete the collection.
	//
	// The foreign key is still the guard here. Everything filed in the collection
	// was just deleted or moved out, so it has nothing left to block, and if
	// something arrived in between the constraint refuses and the rollback restores
	// the items that were already touched.
	if _, err := qtx.DeleteCollectionByIDForUser(
		ctx,
		collectiondb.DeleteCollectionByIDForUserParams{
			ID:     params.CollectionID,
			UserID: params.UserID,
		},
	); err != nil {
		// No row means the collection was removed by a concurrent request that
		// committed while this transaction was working through the items. That is an
		// ordinary outcome for a destructive operation two callers can ask for at
		// once, and it is the same not found the caller would have been given had it
		// arrived a moment later. Reporting it as a server fault would be wrong, and
		// the rollback means this transaction's item changes are undone rather than
		// left applied to a collection nobody has.
		if errors.Is(err, pgx.ErrNoRows) {
			return apperror.NotFoundWith(
				CodeCollectionNotFound,
				"collection not found",
				err,
			)
		}

		return mapCollectionDeleteError(err)
	}

	// 5. Commit. The items and the collection become visible together or not at all.
	if err := tx.Commit(ctx); err != nil {
		return apperror.Internal(err)
	}

	return nil
}

// isUnsorted reports whether a collection's system_key identifies the Unsorted
// collection.
//
// The comparison is exact and NULL-safe. Valid is checked before the value, so a
// collection with no key at all, which is what collections_system_key_check gives
// every user collection, is not compared: comparing an unset value would mean
// deciding what an absent identity is, and every user collection would have to be
// thought about here for no reason.
//
// The display name and the type are both deliberately absent from this decision.
// A name is free text a client renders, and 'system' describes who created a
// collection rather than what it is, so a collection automatic organization
// created is one the user may delete exactly like one they named themselves.
func isUnsorted(systemKey pgtype.Text) bool {
	return systemKey.Valid &&
		systemKey.String == string(CollectionSystemKeyUnsorted)
}

// mapCollectionDeleteError translates a driver error from the final collection
// deletion into an application error.
//
// The foreign key violation is the interesting case. It means a saved item still
// references the collection, which can only happen if one was filed into it after
// this operation had already dealt with the items it found. That is a real business
// outcome and not a server fault, so it is reported as a conflict and the
// transaction rolls back: the collection and its items are exactly as they were and
// the caller can retry.
//
// The constraint is matched by name rather than by error class so this stays tied to
// the specific relationship the application relies on. Nothing else produces a
// foreign key violation here, and an unrelated database error must not be reported
// as a collection that is merely not empty.
func mapCollectionDeleteError(err error) error {
	var pgErr *pgconn.PgError

	if errors.As(err, &pgErr) {
		if pgErr.ConstraintName == savedItemsCollectionFKConstraint {
			return apperror.ConflictWith(
				CodeCollectionNotEmpty,
				"collection is not empty",
				err,
			)
		}
	}

	return apperror.Internal(err)
}

// mapCollectionChildrenError translates a driver error from the statements that
// dispose of a collection's saved items.
//
// These statements write only saved_items, so a foreign key violation here is the
// saved_items side of the same relationship: the target collection of a move went
// away, or was made unusable, between resolving it and acting on it. That is a
// target the caller named and cannot now use, so it is reported as a target that
// cannot be found rather than as a collection that is not empty. Both outcomes roll
// the transaction back, leaving the source collection and its items untouched.
func mapCollectionChildrenError(err error) error {
	var pgErr *pgconn.PgError

	if errors.As(err, &pgErr) {
		if pgErr.ConstraintName == savedItemsCollectionFKConstraint {
			return apperror.NotFoundWith(
				CodeCollectionDeleteTargetNotFound,
				"target collection not found",
				err,
			)
		}
	}

	return apperror.Internal(err)
}

// savedItemsCollectionFKConstraint is the name of the relationship from saved_items
// to collections that protects a collection's saved items. It is named here so the
// error mapping reads as being about that specific constraint.
const savedItemsCollectionFKConstraint = "saved_items_collection_id_fkey"

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
