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

	// GetCollectionByIDForUser resolves one collection owned by the authenticated user,
	// or reports the same not found error for one that does not exist and one owned by
	// somebody else.
	GetCollectionByIDForUser(
		ctx context.Context,
		params GetCollectionByIDForUserParams,
	) (collectiondb.Collection, error)

	// ListCollections returns one page of the user's collections in the requested
	// order, plus whether more rows follow.
	//
	// The cursor arrives already decoded and validated, and the repository never sees
	// a token: decoding is a service concern, and this deals in rows and positions.
	// One row beyond Limit is requested so HasMore is exact rather than inferred from
	// a count.
	ListCollections(
		ctx context.Context,
		params ListCollectionsParams,
	) (ListCollectionsResult, error)

	// ListSavedItemsInCollection returns one page of the saved items in a collection,
	// newest first, plus whether more rows follow.
	//
	// The cursor arrives already decoded, validated and parsed, so this never
	// interprets a client-supplied value.
	ListSavedItemsInCollection(
		ctx context.Context,
		params ListSavedItemsInCollectionParams,
	) (ListSavedItemsInCollectionResult, error)

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

// ListCollections returns one page of the user's collections.
//
// Six statements rather than one, and the count is the design rather than an
// accident. The listing has three independent dimensions: which sort, and whether
// the page starts at the pinned row or resumes from a position. Each combination
// needs its own typed parameter set, because sqlc derives parameter types from the
// statement and a runtime-selected ORDER BY would either collapse them into
// untyped values or force a query-building abstraction this feature has no use for.
// The collection feature already prefers explicit statements for the same reason:
// CreateUserCollection and GetUserCollectionByNameForUser are separate rather than
// one statement with a branch.
//
// No transaction is needed. A listing reads, so there is nothing to keep consistent,
// and each page is a single statement with its own snapshot.
//
// The lookup is always limit + 1. HasMore is therefore a fact about rows that exist
// rather than an inference from a count, and the extra row is dropped before the
// page is returned. Deriving the next cursor from the last returned row, rather than
// from this lookahead row, is what keeps a page boundary from skipping a collection:
// the lookahead row was never sent, so a cursor built from it would resume past it.
func (r *repository) ListCollections(
	ctx context.Context,
	params ListCollectionsParams,
) (ListCollectionsResult, error) {
	rows, err := r.listCollectionsRows(ctx, params)
	if err != nil {
		return ListCollectionsResult{}, err
	}

	hasMore := len(rows) > params.Limit
	if hasMore {
		rows = rows[:params.Limit]
	}

	result := ListCollectionsResult{
		Collections: rows,
		Limit:       params.Limit,
		Sort:        params.Sort,
		HasMore:     hasMore,
	}

	return result, nil
}

// listCollectionsRows dispatches to the statement matching the requested sort and
// resume point.
//
// The after-Unsorted statements take no position at all. A cursor in that group
// means the previous page ended on the pinned row, so this page starts at the top of
// the regular ordering. There is deliberately nothing to pass: a zero timestamp
// would build a predicate against '-0001-01-01' and return the wrong rows, and
// keeping these separate makes that impossible rather than guarded at runtime.
func (r *repository) listCollectionsRows(
	ctx context.Context,
	params ListCollectionsParams,
) ([]collectiondb.Collection, error) {
	// One row beyond the limit, so the caller can tell whether more exist.
	pageLimit := int32(params.Limit + 1)

	switch {
	case params.Cursor == nil:
		return r.listFirstPage(ctx, params, pageLimit)

	case !params.Cursor.hasPosition():
		return r.listAfterUnsorted(ctx, params, pageLimit)

	default:
		return r.listAfterPosition(ctx, params, pageLimit)
	}
}

func (r *repository) listFirstPage(
	ctx context.Context,
	params ListCollectionsParams,
	pageLimit int32,
) ([]collectiondb.Collection, error) {
	var (
		rows []collectiondb.Collection
		err  error
	)

	switch params.Sort {
	case CollectionSortOldest:
		rows, err = r.queries.ListCollectionsOldestFirst(
			ctx,
			collectiondb.ListCollectionsOldestFirstParams{
				UserID:    params.UserID,
				PageLimit: pageLimit,
			},
		)

	case CollectionSortName:
		rows, err = r.queries.ListCollectionsByNameFirst(
			ctx,
			collectiondb.ListCollectionsByNameFirstParams{
				UserID:    params.UserID,
				PageLimit: pageLimit,
			},
		)

	default:
		rows, err = r.queries.ListCollectionsNewestFirst(
			ctx,
			collectiondb.ListCollectionsNewestFirstParams{
				UserID:    params.UserID,
				PageLimit: pageLimit,
			},
		)
	}

	if err != nil {
		return nil, internalError(err)
	}

	return rows, nil
}

func (r *repository) listAfterUnsorted(
	ctx context.Context,
	params ListCollectionsParams,
	pageLimit int32,
) ([]collectiondb.Collection, error) {
	var (
		rows []collectiondb.Collection
		err  error
	)

	switch params.Sort {
	case CollectionSortOldest:
		rows, err = r.queries.ListCollectionsOldestAfterUnsorted(
			ctx,
			collectiondb.ListCollectionsOldestAfterUnsortedParams{
				UserID:    params.UserID,
				PageLimit: pageLimit,
			},
		)

	case CollectionSortName:
		rows, err = r.queries.ListCollectionsByNameAfterUnsorted(
			ctx,
			collectiondb.ListCollectionsByNameAfterUnsortedParams{
				UserID:    params.UserID,
				PageLimit: pageLimit,
			},
		)

	default:
		rows, err = r.queries.ListCollectionsNewestAfterUnsorted(
			ctx,
			collectiondb.ListCollectionsNewestAfterUnsortedParams{
				UserID:    params.UserID,
				PageLimit: pageLimit,
			},
		)
	}

	if err != nil {
		return nil, internalError(err)
	}

	return rows, nil
}

func (r *repository) listAfterPosition(
	ctx context.Context,
	params ListCollectionsParams,
	pageLimit int32,
) ([]collectiondb.Collection, error) {
	// Both halves of the position were validated and, for the time-based sorts,
	// parsed by the service before this was called. The repository passes them on
	// without reinterpreting them: CursorValue is already the type sqlc generated for
	// a timestamptz column, and for the name sort it is already the normalized form
	// the ordering and the unique index compute. A token that survived validation can
	// therefore not become a query error here.
	cursorID := *params.Cursor.ID

	switch params.Sort {
	case CollectionSortOldest:
		rows, err := r.queries.ListCollectionsOldestAfterRow(
			ctx,
			collectiondb.ListCollectionsOldestAfterRowParams{
				UserID:      params.UserID,
				CursorValue: params.Cursor.Timestamp,
				CursorID:    cursorID,
				PageLimit:   pageLimit,
			},
		)
		if err != nil {
			return nil, internalError(err)
		}

		return rows, nil

	case CollectionSortName:
		rows, err := r.queries.ListCollectionsByNameAfterRow(
			ctx,
			collectiondb.ListCollectionsByNameAfterRowParams{
				UserID:      params.UserID,
				CursorValue: *params.Cursor.Value,
				CursorID:    cursorID,
				PageLimit:   pageLimit,
			},
		)
		if err != nil {
			return nil, internalError(err)
		}

		return rows, nil

	default:
		rows, err := r.queries.ListCollectionsNewestAfterRow(
			ctx,
			collectiondb.ListCollectionsNewestAfterRowParams{
				UserID:      params.UserID,
				CursorValue: params.Cursor.Timestamp,
				CursorID:    cursorID,
				PageLimit:   pageLimit,
			},
		)
		if err != nil {
			return nil, internalError(err)
		}

		return rows, nil
	}
}

// GetCollectionByIDForUser resolves one collection of the authenticated user.
//
// A collection that does not exist and one owned by somebody else both match nothing,
// so both produce the same not found error. That is the non-disclosure guarantee the
// move and delete paths already rely on, exposed here so a listing can prove
// ownership the same way rather than assuming it from the id it was given.
func (r *repository) GetCollectionByIDForUser(
	ctx context.Context,
	params GetCollectionByIDForUserParams,
) (collectiondb.Collection, error) {
	collection, err := r.queries.GetCollectionByIDForUser(
		ctx,
		collectiondb.GetCollectionByIDForUserParams{
			ID:     params.ID,
			UserID: params.UserID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return collectiondb.Collection{}, apperror.NotFoundWith(
				CodeCollectionNotFound,
				"collection not found",
				err,
			)
		}

		return collectiondb.Collection{}, internalError(err)
	}

	return collection, nil
}

// ListSavedItemsInCollection returns one page of the saved items in a collection.
//
// One row beyond the limit is requested so HasMore is a fact about rows that exist
// rather than an inference from a count. The extra row is dropped before the page is
// returned, and the caller builds the next cursor from the last row actually returned,
// never from this lookahead row: the lookahead row was never sent, so a cursor built
// from it would resume past an item the client never received.
//
// No transaction is needed. A listing reads, so there is nothing to keep consistent,
// and the page is a single statement with its own snapshot.
func (r *repository) ListSavedItemsInCollection(
	ctx context.Context,
	params ListSavedItemsInCollectionParams,
) (ListSavedItemsInCollectionResult, error) {
	pageLimit := int32(params.Limit + 1)

	rows, err := r.listSavedItemRows(ctx, params, pageLimit)
	if err != nil {
		return ListSavedItemsInCollectionResult{}, err
	}

	hasMore := len(rows) > params.Limit
	if hasMore {
		rows = rows[:params.Limit]
	}

	return ListSavedItemsInCollectionResult{
		SavedItems: rows,
		Limit:      params.Limit,
		HasMore:    hasMore,
	}, nil
}

// listSavedItemRows dispatches to the statement matching the resume point.
//
// Unlike the collection listing there are only two, not six: this listing has a
// single ordering and no pinned row, so there is no sort to branch on and no
// positionless group.
func (r *repository) listSavedItemRows(
	ctx context.Context,
	params ListSavedItemsInCollectionParams,
	pageLimit int32,
) ([]ListedSavedItem, error) {
	if params.Cursor == nil {
		rows, err := r.queries.ListSavedItemsInCollectionFirst(
			ctx,
			collectiondb.ListSavedItemsInCollectionFirstParams{
				CollectionID: params.CollectionID,
				UserID:       params.UserID,
				PageLimit:    pageLimit,
			},
		)
		if err != nil {
			return nil, internalError(err)
		}

		return newListedSavedItems(rows), nil
	}

	// Both halves of the position were validated and the timestamp parsed by the
	// service before this was called. CursorValue is therefore already the type sqlc
	// generated for a timestamptz column, and a token that survived validation cannot
	// become a query error here.
	rows, err := r.queries.ListSavedItemsInCollectionAfterRow(
		ctx,
		collectiondb.ListSavedItemsInCollectionAfterRowParams{
			CollectionID: params.CollectionID,
			UserID:       params.UserID,
			CursorValue:  params.Cursor.Timestamp,
			CursorID:     *params.Cursor.ID,
			PageLimit:    pageLimit,
		},
	)
	if err != nil {
		return nil, internalError(err)
	}

	return newListedSavedItemsAfterCursor(rows), nil
}

// newListedSavedItems maps the first-page rows onto the listing projection.
//
// Made with a length rather than left nil so an empty page serializes as [] rather
// than null, which is the shape a caller decoding this list expects.
func newListedSavedItems(
	rows []collectiondb.ListSavedItemsInCollectionFirstRow,
) []ListedSavedItem {
	savedItems := make([]ListedSavedItem, 0, len(rows))

	for _, row := range rows {
		savedItems = append(savedItems, newListedSavedItem(row))
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
	rows []collectiondb.ListSavedItemsInCollectionAfterRowRow,
) []ListedSavedItem {
	savedItems := make([]ListedSavedItem, 0, len(rows))

	for _, row := range rows {
		savedItems = append(
			savedItems,
			ListedSavedItem{
				ID:               row.ID,
				UserID:           row.UserID,
				URL:              row.Url,
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
			},
		)
	}

	return savedItems
}

func newListedSavedItem(
	row collectiondb.ListSavedItemsInCollectionFirstRow,
) ListedSavedItem {
	return ListedSavedItem{
		ID:               row.ID,
		UserID:           row.UserID,
		URL:              row.Url,
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

// internalError turns a driver error into an application error this repository
// cannot describe more specifically.
//
// It is defined here so the listing and the collection deletion share one
// translation for the failures they have nothing further to say about.
func internalError(err error) error {
	return apperror.Internal(err)
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
