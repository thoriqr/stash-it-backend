package organization

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	organizationdb "github.com/thoriqr/stash-it-backend/internal/worker/organization/generated"
)

// Repository is the worker's own persistence for automatic organization.
//
// It is a separate interface from internal/api/collection.Repository and from the
// enrichment worker's, on the same reasoning the enrichment worker documents: the
// two run in different binaries against different sqlc targets, and the statements
// differ enough that sharing them would mean sharing projections and transaction
// shapes neither side actually wants.
//
// The whole operation is one method because its halves cannot be split across
// calls: the decision has to be made against a locked row, and the collection
// created and the item moved have to become visible together.
type Repository interface {
	// OrganizeSavedItem files one saved item into the collection its platform
	// names, creating that collection when it does not exist.
	//
	// It returns an Outcome describing what it did, so a caller can log the real
	// reason a task finished rather than infer it from the absence of an error. A
	// terminal no-op is not an error; only a missing item, an ownership mismatch, a
	// reserved name, or a database problem is.
	OrganizeSavedItem(
		ctx context.Context,
		savedItemID uuid.UUID,
		userID uuid.UUID,
	) (Outcome, error)
}

type repository struct {
	pool    *pgxpool.Pool
	queries *organizationdb.Queries
}

func NewRepository(
	pool *pgxpool.Pool,
	queries *organizationdb.Queries,
) Repository {
	return &repository{
		pool:    pool,
		queries: queries,
	}
}

// OrganizeSavedItem files one saved item into the collection its platform names.
//
// The whole operation is one transaction owned here, for two reasons that are
// really one. Collection creation and the move have to be all-or-nothing: a
// committed collection holding nothing would be a system collection created for
// no reason, and an empty one is not a state this product has anywhere else. And
// the decision has to be made against state that cannot change underneath it,
// which is what the lock at the start buys.
//
// The sequence:
//
//  1. Lock the saved item, scoped to the task's user. No row here means either the
//     item is gone or it belongs to somebody else, and the two are told apart by
//     a second read rather than by a single sentinel.
//  2. Decide, from that locked row, whether there is anything to do.
//  3. Resolve the target collection, creating it only if the name is free.
//  4. Move the item, unless it is already there.
//
// Ownership is asserted twice on purpose. The lock is scoped by user_id, and so is
// the write that follows, and the composite foreign key from migration 000025
// makes the database check the same thing again. None of the three can be
// satisfied while the others are not.
func (r *repository) OrganizeSavedItem(
	ctx context.Context,
	savedItemID uuid.UUID,
	userID uuid.UUID,
) (Outcome, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return OutcomeUnknown, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	// 1. Lock the item, and prove the task's claim about who owns it.
	//
	// The lock is taken before anything is read, so the decision below and the
	// write that acts on it are one atomic fact rather than two that a concurrent
	// user action can slip between. A user filing this item into their own
	// collection blocks here until this transaction is done, and then this
	// transaction's write is what both of them serialize on.
	locked, err := qtx.LockSavedItemForOrganization(ctx, savedItemID)
	if err != nil {
		// No row means the item was deleted between enrichment completing and this
		// task running. That is an ordinary outcome for a queue whose tasks are
		// processed later than they were created, and it is reported as the
		// sentinel the handler discards on.
		if errors.Is(err, pgx.ErrNoRows) {
			return OutcomeUnknown, notFoundError(err)
		}

		return OutcomeUnknown, apperror.Internal(err)
	}

	savedItem := newSavedItem(locked)

	// Ownership is verified here rather than in the statement's WHERE clause,
	// because the two outcomes have to be told apart and a scoped lookup cannot:
	// a missing item and a foreign one would both return no row. Reading by id and
	// comparing is what makes "somebody else's item" distinguishable from
	// "no item at all".
	//
	// It cannot happen through any path the product has, since every task is
	// scheduled by the enrichment worker for the item's own owner. It is reported
	// rather than discarded for that reason: an item left untouched would hide a
	// bug that files one user's item into another user's collection.
	if savedItem.UserID != userID {
		return OutcomeUnknown, ownershipMismatchError(savedItemID, userID)
	}

	// 2. Decide, from the locked row, whether there is anything to do.
	//
	// Every one of these is a successful completion rather than a failure. The
	// item is valid either way, and retrying cannot change the answer: the page
	// will still expose no platform, and the user will still have filed the item
	// where they want it.
	switch {
	case !savedItem.IsEnrichmentCompleted():
		return OutcomeEnrichmentNotCompleted, finish(ctx, tx)

	case !savedItem.HasPlatform():
		return OutcomeNoPlatform, finish(ctx, tx)

	case !savedItem.IsInUnsorted():
		// The rule that makes organization additive. The item was in Unsorted
		// when enrichment finished, which says nothing about where it is now, and
		// the user's filing is the newer decision. Moving it back would override
		// a choice made deliberately with the whole item in front of them.
		return OutcomeNotInUnsorted, finish(ctx, tx)
	}

	// 3. Resolve the target collection, creating it only if the name is free.
	platform := savedItem.Platform.String

	collection, created, err := createOrGetCollection(
		ctx,
		qtx,
		userID,
		platform,
	)
	if err != nil {
		return OutcomeUnknown, err
	}

	// 4. Move the item, unless it is already there.
	//
	// The item was locked with collection_id in hand, so this comparison is the
	// current truth rather than a remembered one. Skipping the write is what makes
	// a repeated delivery free of side effects: saved_items has an updated_at
	// trigger, so rewriting collection_id with the value it already holds would
	// make a no-op indistinguishable from a real move.
	if savedItem.CollectionID == collection.ID {
		return OutcomeAlreadyOrganized, finish(ctx, tx)
	}

	_, err = qtx.MoveSavedItemToCollectionForOrganization(
		ctx,
		organizationdb.MoveSavedItemToCollectionForOrganizationParams{
			CollectionID: collection.ID,
			ID:           savedItemID,
			UserID:       userID,
		},
	)
	if err != nil {
		// The item was locked and owned a moment ago in this same transaction, so
		// no row here means the row changed despite the lock, which the composite
		// ownership foreign key makes impossible in normal operation. Treating it
		// as retryable is right: if the database is that confused, a later attempt
		// on a fresh snapshot is more likely to succeed than any repair here.
		if errors.Is(err, pgx.ErrNoRows) {
			return OutcomeUnknown, apperror.Internal(
				errors.New(
					"locked saved item disappeared before it could be filed",
				),
			)
		}

		return OutcomeUnknown, apperror.Internal(err)
	}

	outcome := OutcomeMoved
	if created {
		outcome = OutcomeCollectionCreated
	}

	return outcome, finish(ctx, tx)
}

// createOrGetCollection resolves the user's collection named by the platform,
// creating a system collection when the name is free.
//
// This is two statements for the same reason the API collection feature uses two:
// the insert returns no row when the name is taken, and the lookup that follows
// takes a fresh snapshot under READ COMMITTED, so it also sees a collection that a
// concurrent transaction committed while this one waited on the conflicting index
// entry. A data-modifying CTE cannot be used instead, because it shares one
// snapshot with its own SELECT and would return nothing in that case.
//
// The unsorted name is refused rather than matched. Filing an item into Unsorted
// would move it nowhere and report success, so the reserved name is reported
// instead of silently doing nothing.
func createOrGetCollection(
	ctx context.Context,
	qtx *organizationdb.Queries,
	userID uuid.UUID,
	platform string,
) (
	Collection,
	bool,
	error,
) {
	// The platform becomes a collection's display name, so it is trimmed the way
	// the collection API trims a submitted name. The stored name and the
	// lower(btrim(name)) the unique index compares have to agree on what the name
	// is, or a collection created here would carry a name the database considers
	// different from the one it was matched on. It is not lowercased: the name is
	// shown to a person, and case-insensitive uniqueness is the index's decision.
	name := strings.TrimSpace(platform)

	// The unsorted name is refused rather than matched. Filing an item into Unsorted
	// would move it nowhere and report success, so the reserved name is reported
	// instead of silently doing nothing. It is compared the way the name index
	// compares, so a platform spelled "Unsorted", "unsorted" or " UNSORTED " is
	// the same reserved name.
	if normalizeCollectionName(name) ==
		normalizeCollectionName(CollectionSystemKeyUnsorted) {
		return Collection{}, false, reservedNameError()
	}

	existing, err := qtx.GetCollectionByNameForOrganization(
		ctx,
		organizationdb.GetCollectionByNameForOrganizationParams{
			UserID: userID,
			Name:   name,
		},
	)
	if err == nil {
		// A collection of either type is a legitimate target. Reusing one the user
		// named themselves is the behavior, not an exception to it: their naming is
		// theirs to choose, and filing into "youtube" when the page calls the
		// platform "YouTube" respects that instead of duplicating or renaming it.
		return newCollection(existing), false, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return Collection{}, false, apperror.Internal(err)
	}

	created, err := qtx.CreateSystemCollectionForOrganization(
		ctx,
		organizationdb.CreateSystemCollectionForOrganizationParams{
			UserID: userID,
			Name:   name,
			// The platform's own normalized identity, which is the same reduction
			// the name index applies. collections_system_key_check requires a system
			// collection to carry a key, and collections_system_key_unique then
			// guarantees one such collection per platform per user.
			SystemKey: pgtype.Text{
				String: SystemKeyForPlatform(name),
				Valid:  true,
			},
		},
	)
	if err == nil {
		return newCollectionFromCreate(created), true, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return Collection{}, false, apperror.Internal(err)
	}

	// Another task created it while this one waited. The unique index is what
	// decided it, so the row is known to exist and only its identity has to be
	// read. The same snapshot caveat applies as above: this lookup sees the
	// winner's committed row.
	raced, err := qtx.GetCollectionByNameForOrganization(
		ctx,
		organizationdb.GetCollectionByNameForOrganizationParams{
			UserID: userID,
			Name:   name,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Collection{}, false, reservedNameError()
		}

		return Collection{}, false, apperror.Internal(err)
	}

	return newCollection(raced), false, nil
}

// finish commits and reports any commit failure.
//
// Every no-op path commits rather than rolling back, because the transaction may
// hold nothing but the lock, and a rollback would be reported as a failure for an
// outcome that is simply finished. Committing releases the lock either way.
func finish(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Commit(ctx); err != nil {
		return apperror.Internal(err)
	}

	return nil
}

func newSavedItem(
	row organizationdb.LockSavedItemForOrganizationRow,
) SavedItem {
	return SavedItem{
		ID:                  row.ID,
		UserID:              row.UserID,
		EnrichmentStatus:    row.EnrichmentStatus,
		Platform:            row.Platform,
		CollectionID:        row.CollectionID,
		CollectionSystemKey: row.CollectionSystemKey,
	}
}

func newCollection(
	row organizationdb.GetCollectionByNameForOrganizationRow,
) Collection {
	return Collection{
		ID:        row.ID,
		UserID:    row.UserID,
		Name:      row.Name,
		Type:      row.Type,
		SystemKey: row.SystemKey,
	}
}

func newCollectionFromCreate(
	row organizationdb.CreateSystemCollectionForOrganizationRow,
) Collection {
	return Collection{
		ID:        row.ID,
		UserID:    row.UserID,
		Name:      row.Name,
		Type:      row.Type,
		SystemKey: row.SystemKey,
	}
}

// notFoundError builds the error a missing saved item produces.
//
// It carries the code as well as the sentinel, because the sentinel alone loses
// the code through AppError.Unwrap for anything that inspects the code. The
// handler matches on ErrSavedItemNotFound.
func notFoundError(err error) error {
	return apperror.NotFoundWith(
		CodeSavedItemNotFound,
		"saved item not found",
		ErrSavedItemNotFound,
	)
}

// ownershipMismatchError reports that a task named an item belonging to someone
// else.
//
// The ids are part of the cause rather than the message: the handler's log line
// already carries both, and the message is what would reach anything rendering
// the error to a person.
func ownershipMismatchError(
	savedItemID uuid.UUID,
	userID uuid.UUID,
) error {
	return apperror.ConflictWith(
		CodeSavedItemOwnershipMismatch,
		"saved item does not belong to the task's user",
		errors.Join(
			ErrSavedItemOwnershipMismatch,
			fmt.Errorf(
				"saved item %s is not owned by user %s",
				savedItemID,
				userID,
			),
		),
	)
}

// reservedNameError is reported when the platform's name cannot be a target.
//
// It carries no internal detail beyond the name involved, which is a value the
// page itself published and is already stored on the item.
func reservedNameError() error {
	return apperror.ConflictWith(
		CodeCollectionNameReserved,
		"collection name is reserved",
		ErrCollectionNameTakenByReservedName,
	)
}
