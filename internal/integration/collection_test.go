package integration_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/collection"
	collectiondb "github.com/thoriqr/stash-it-backend/internal/api/collection/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	collectiondbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/collection/generated"
)

// newCollectionService builds the real collection service against the shared test
// pool. There is no HTTP endpoint for this use case yet, so these tests drive the
// service directly, which is also where the transaction lives.
func newCollectionService(t *testing.T) collection.Service {
	t.Helper()

	queries := collectiondb.New(testPool)

	return collection.NewService(
		collection.NewRepository(
			testPool,
			queries,
		),
	)
}

func truncateCollectionData(t *testing.T) {
	t.Helper()

	db := collectiondbtest.New(testPool)

	require.NoError(t, db.TruncateCollectionData(context.Background()))
}

func createCollectionUser(t *testing.T, email string) uuid.UUID {
	t.Helper()

	ctx := context.Background()
	db := collectiondbtest.New(testPool)

	userID, err := db.CreateCollectionUser(
		ctx,
		collectiondbtest.CreateCollectionUserParams{
			Email:       email,
			DisplayName: "Collection Test User",
		},
	)
	require.NoError(t, err)

	return userID
}

// createUnsorted creates the Unsorted system collection that every permanent user
// receives when their registration is finalized. Tests seed it directly because the
// registration flow is out of scope here.
func createUnsorted(t *testing.T, userID uuid.UUID) uuid.UUID {
	t.Helper()

	ctx := context.Background()
	db := collectiondbtest.New(testPool)

	collectionID, err := db.CreateUnsortedCollection(ctx, userID)
	require.NoError(t, err)

	return collectionID
}

func createSavedItemInCollection(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
	url string,
) uuid.UUID {
	t.Helper()

	ctx := context.Background()
	db := collectiondbtest.New(testPool)

	savedItemID, err := db.CreateTestSavedItemInCollection(
		ctx,
		collectiondbtest.CreateTestSavedItemInCollectionParams{
			UserID:       userID,
			Url:          url,
			Domain:       testText("example.com"),
			Platform:     pgtype.Text{},
			Title:        pgtype.Text{},
			CollectionID: collectionID,
		},
	)
	require.NoError(t, err)

	return savedItemID
}

func countCollectionsNamed(t *testing.T, userID uuid.UUID, name string) int64 {
	t.Helper()

	ctx := context.Background()
	db := collectiondbtest.New(testPool)

	count, err := db.CountCollectionsNamedForUser(
		ctx,
		collectiondbtest.CountCollectionsNamedForUserParams{
			UserID: userID,
			Name:   name,
		},
	)
	require.NoError(t, err)

	return count
}

func savedItemState(
	t *testing.T,
	savedItemID uuid.UUID,
) collectiondbtest.GetSavedItemCollectionStateRow {
	t.Helper()

	ctx := context.Background()
	db := collectiondbtest.New(testPool)

	state, err := db.GetSavedItemCollectionState(ctx, savedItemID)
	require.NoError(t, err)

	return state
}

func TestCollection_PutSavedItem(t *testing.T) {
	t.Run("creates the collection and moves the saved item into it", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		userID := createCollectionUser(t, "collection-create@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/articles/1",
		)

		result, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"Wishlist",
		)
		require.NoError(t, err)

		require.True(t, result.CollectionCreated)
		require.False(t, result.AlreadyInCollection)

		// The collection is a user collection: type = 'user', system_key IS NULL.
		require.Equal(t, string(collection.CollectionTypeUser), result.Collection.Type)
		require.False(t, result.Collection.SystemKey.Valid)
		require.Equal(t, "Wishlist", result.Collection.Name)
		require.Equal(t, userID, result.Collection.UserID)

		// Exactly one collection carries this name for this user.
		require.Equal(t, int64(1), countCollectionsNamed(t, userID, "Wishlist"))

		// The saved item now points at the new collection.
		state := savedItemState(t, savedItemID)
		require.Equal(t, result.Collection.ID, state.CollectionID)
		require.Equal(t, userID, state.UserID)

		// And the projection the service returns agrees with the row.
		require.Equal(t, result.Collection.ID, result.SavedItem.CollectionID)
		require.Equal(t, savedItemID, result.SavedItem.ID)
	})

	t.Run("reuses an existing collection instead of creating another", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		userID := createCollectionUser(t, "collection-reuse@example.com")
		unsortedID := createUnsorted(t, userID)

		firstItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/first",
		)
		secondItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/second",
		)

		first, err := svc.PutSavedItem(
			context.Background(),
			userID,
			firstItemID,
			"Wishlist",
		)
		require.NoError(t, err)
		require.True(t, first.CollectionCreated)

		before, err := collectiondbtest.New(testPool).GetCollectionState(
			context.Background(),
			first.Collection.ID,
		)
		require.NoError(t, err)

		// A differently cased, differently padded name must resolve to the same
		// collection, because collections_user_name_unique compares
		// lower(btrim(name)).
		second, err := svc.PutSavedItem(
			context.Background(),
			userID,
			secondItemID,
			"  wishLIST  ",
		)
		require.NoError(t, err)

		require.False(
			t,
			second.CollectionCreated,
			"the second call must not create another collection",
		)
		require.False(t, second.AlreadyInCollection)
		require.Equal(t, first.Collection.ID, second.Collection.ID)

		// Still exactly one row for the name.
		require.Equal(t, int64(1), countCollectionsNamed(t, userID, "Wishlist"))
		require.Equal(t, int64(1), countCollectionsNamed(t, userID, "wishlist"))

		// The existing collection was reused, not rewritten. Reusing it must not
		// touch created_at or updated_at, which is why the create-or-get does not
		// use ON CONFLICT DO UPDATE.
		after, err := collectiondbtest.New(testPool).GetCollectionState(
			context.Background(),
			first.Collection.ID,
		)
		require.NoError(t, err)

		require.Equal(t, before.CreatedAt, after.CreatedAt)
		require.Equal(t, before.UpdatedAt, after.UpdatedAt)
		require.Equal(t, "Wishlist", after.Name)

		// The stored display name is the one that was created first: the second call
		// does not rewrite it to the casing it happened to use.
		require.Equal(t, first.Collection.ID, savedItemState(t, secondItemID).CollectionID)
	})

	t.Run("a user collection name is reserved against a system collection", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		userID := createCollectionUser(t, "collection-reserved@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/reserved",
		)

		// "Unsorted" is the display name of a system collection. collections_user_name_unique
		// reserves it, and a user collection must not be created under it either.
		_, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"Unsorted",
		)
		require.Error(t, err)

		appErr := apperror.FromError(err)
		require.Equal(t, collection.CodeCollectionNameReserved, appErr.Code)
		require.Equal(t, 409, appErr.Status)

		// The reserved name must not have produced a user collection, and the saved
		// item must still be in Unsorted.
		require.Equal(t, int64(1), countCollectionsNamed(t, userID, "Unsorted"))
		require.Equal(t, unsortedID, savedItemState(t, savedItemID).CollectionID)

		state, err := collectiondbtest.New(testPool).GetCollectionState(
			context.Background(),
			unsortedID,
		)
		require.NoError(t, err)
		require.Equal(
			t,
			string(collection.CollectionTypeSystem),
			state.Type,
		)
		require.True(t, state.SystemKey.Valid)
		require.Equal(
			t,
			string(collection.CollectionSystemKeyUnsorted),
			state.SystemKey.String,
		)
	})

	t.Run("a reserved name is rejected whatever its casing or padding", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		userID := createCollectionUser(t, "collection-reserved-case@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/reserved-case",
		)

		_, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"  uNsOrTeD ",
		)
		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeCollectionNameReserved,
			apperror.FromError(err).Code,
		)

		require.Equal(t, unsortedID, savedItemState(t, savedItemID).CollectionID)
	})

	t.Run("another user's saved item is not found and creates no collection", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		ownerID := createCollectionUser(t, "collection-owner@example.com")
		otherUserID := createCollectionUser(t, "collection-thief@example.com")

		ownerUnsortedID := createUnsorted(t, ownerID)
		savedItemID := createSavedItemInCollection(
			t,
			ownerID,
			ownerUnsortedID,
			"https://example.com/someone-elses",
		)

		_, err := svc.PutSavedItem(
			context.Background(),
			otherUserID,
			savedItemID,
			"Wishlist",
		)
		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeSavedItemNotFound,
			apperror.FromError(err).Code,
		)
		require.Equal(t, 404, apperror.FromError(err).Status)

		// No collection was created for either user, so the failing move left
		// nothing behind.
		require.Equal(t, int64(0), countCollectionsNamed(t, otherUserID, "Wishlist"))
		require.Equal(t, int64(0), countCollectionsNamed(t, ownerID, "Wishlist"))

		// The owner's item is untouched and still in Unsorted.
		state := savedItemState(t, savedItemID)
		require.Equal(t, ownerID, state.UserID)
		require.Equal(t, ownerUnsortedID, state.CollectionID)
	})

	t.Run("an unknown saved item returns the same error as a foreign one", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		ownerID := createCollectionUser(t, "collection-unknown-owner@example.com")
		otherUserID := createCollectionUser(t, "collection-unknown-other@example.com")

		ownerUnsortedID := createUnsorted(t, ownerID)
		ownedItemID := createSavedItemInCollection(
			t,
			ownerID,
			ownerUnsortedID,
			"https://example.com/owned",
		)

		describe := func(savedItemID uuid.UUID) (string, string) {
			_, err := svc.PutSavedItem(
				context.Background(),
				otherUserID,
				savedItemID,
				"Wishlist",
			)
			require.Error(t, err)

			appErr := apperror.FromError(err)

			return appErr.Code, appErr.Message
		}

		unknownCode, unknownMessage := describe(uuid.New())
		foreignCode, foreignMessage := describe(ownedItemID)

		// An id that does not exist and an id owned by someone else must be
		// indistinguishable, matching the saved_item non-disclosure behavior.
		require.Equal(t, unknownCode, foreignCode)
		require.Equal(t, unknownMessage, foreignMessage)
		require.Equal(t, collection.CodeSavedItemNotFound, unknownCode)

		// Neither attempt created a collection.
		require.Equal(t, int64(0), countCollectionsNamed(t, otherUserID, "Wishlist"))
	})

	t.Run("an unknown saved item creates no collection", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		userID := createCollectionUser(t, "collection-unknown@example.com")
		createUnsorted(t, userID)

		_, err := svc.PutSavedItem(
			context.Background(),
			userID,
			uuid.New(),
			"Wishlist",
		)
		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeSavedItemNotFound,
			apperror.FromError(err).Code,
		)

		require.Equal(t, int64(0), countCollectionsNamed(t, userID, "Wishlist"))
	})

	t.Run("putting the saved item in again succeeds and writes nothing", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		userID := createCollectionUser(t, "collection-idempotent@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/idempotent",
		)

		first, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"Wishlist",
		)
		require.NoError(t, err)
		require.True(t, first.CollectionCreated)
		require.False(t, first.AlreadyInCollection)

		afterFirst := savedItemState(t, savedItemID)

		second, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"Wishlist",
		)
		require.NoError(t, err)

		require.True(t, second.AlreadyInCollection)
		require.False(t, second.CollectionCreated)
		require.Equal(t, first.Collection.ID, second.Collection.ID)

		// saved_items has an updated_at trigger, so an UPDATE here would bump
		// updated_at and make a no-op look like a real move.
		afterSecond := savedItemState(t, savedItemID)
		require.Equal(t, afterFirst.UpdatedAt, afterSecond.UpdatedAt)
		require.Equal(t, first.Collection.ID, afterSecond.CollectionID)

		// And the collection itself was not rewritten either.
		require.Equal(t, int64(1), countCollectionsNamed(t, userID, "Wishlist"))
	})

	t.Run("a differently cased name for the current collection is a no-op", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		userID := createCollectionUser(t, "collection-idempotent-case@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/idempotent-case",
		)

		first, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"Wishlist",
		)
		require.NoError(t, err)

		before := savedItemState(t, savedItemID)

		// Same collection by the unique index's comparison, so still a no-op.
		second, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"WISHLIST",
		)
		require.NoError(t, err)

		require.True(t, second.AlreadyInCollection)
		require.Equal(t, first.Collection.ID, second.Collection.ID)
		require.Equal(t, before.UpdatedAt, savedItemState(t, savedItemID).UpdatedAt)
	})

	t.Run("succeeds regardless of enrichment state and leaves it untouched", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		enrichmentStartedAt := pgtype.Timestamptz{
			Time:  timeAt(t, -2*time.Hour),
			Valid: true,
		}
		lastEnrichedAt := pgtype.Timestamptz{
			Time:  timeAt(t, -1*time.Hour),
			Valid: true,
		}

		// Every value the enrichment_status CHECK allows.
		statuses := []string{
			"pending",
			"processing",
			"completed",
			"failed",
		}

		userID := createCollectionUser(t, "collection-enrichment@example.com")
		unsortedID := createUnsorted(t, userID)

		for i, status := range statuses {
			savedItemID := createSavedItemInCollection(
				t,
				userID,
				unsortedID,
				"https://example.com/enrichment/"+string(rune('a'+i)),
			)

			// Give every item a non-default enrichment state so the assertion is
			// about untouched values, not untouched defaults.
			require.NoError(
				t,
				db.SetTestSavedItemEnrichmentState(
					ctx,
					collectiondbtest.SetTestSavedItemEnrichmentStateParams{
						ID:                  savedItemID,
						EnrichmentStatus:    status,
						EnrichmentStartedAt: enrichmentStartedAt,
						LastEnrichedAt:      lastEnrichedAt,
					},
				),
			)

			before := savedItemState(t, savedItemID)
			require.Equal(t, status, before.EnrichmentStatus)

			result, err := svc.PutSavedItem(
				ctx,
				userID,
				savedItemID,
				"Wishlist",
			)
			require.NoError(t, err, status)
			require.Equal(t, result.Collection.ID, savedItemState(t, savedItemID).CollectionID)

			// Enrichment is unrelated to this operation: the move must not read it,
			// and must leave every enrichment column byte-identical.
			after := savedItemState(t, savedItemID)
			require.Equal(t, before.EnrichmentStatus, after.EnrichmentStatus)
			require.Equal(t, before.EnrichmentStartedAt, after.EnrichmentStartedAt)
			require.Equal(t, before.LastEnrichedAt, after.LastEnrichedAt)
		}
	})

	t.Run("a pending item can be moved into an existing collection", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		userID := createCollectionUser(t, "collection-pending@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/pending",
		)

		existing, err := db.CreateTestUserCollection(
			ctx,
			collectiondbtest.CreateTestUserCollectionParams{
				UserID: userID,
				Name:   "Wishlist",
			},
		)
		require.NoError(t, err)

		result, err := svc.PutSavedItem(
			ctx,
			userID,
			savedItemID,
			"Wishlist",
		)
		require.NoError(t, err)

		require.False(t, result.CollectionCreated)
		require.Equal(t, existing.ID, result.Collection.ID)

		// enrichment_status stays at its column default: nothing set it, and the
		// move did not either.
		after := savedItemState(t, savedItemID)
		require.Equal(t, "pending", after.EnrichmentStatus)
		require.False(t, after.EnrichmentStartedAt.Valid)
		require.False(t, after.LastEnrichedAt.Valid)
	})

	t.Run("Unsorted is left in place and may become empty", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		userID := createCollectionUser(t, "collection-unsorted-empty@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/last-item",
		)

		result, err := svc.PutSavedItem(ctx, userID, savedItemID, "Wishlist")
		require.NoError(t, err)

		// The only item left Unsorted, which is allowed: Unsorted may be empty but
		// is never removed.
		require.Equal(t, result.Collection.ID, savedItemState(t, savedItemID).CollectionID)
		require.NotEqual(t, unsortedID, result.Collection.ID)
		require.Equal(t, int64(0), countSavedItemsInCollection(t, unsortedID))
		require.Equal(t, int64(1), countSavedItemsInCollection(t, result.Collection.ID))

		unsorted, err := db.GetCollectionState(ctx, unsortedID)
		require.NoError(t, err)

		require.Equal(t, string(collection.CollectionTypeSystem), unsorted.Type)
		require.Equal(
			t,
			string(collection.CollectionSystemKeyUnsorted),
			unsorted.SystemKey.String,
		)

		count, err := db.CountSystemCollectionsByKey(
			ctx,
			collectiondbtest.CountSystemCollectionsByKeyParams{
				UserID:    userID,
				SystemKey: testText(string(collection.CollectionSystemKeyUnsorted)),
			},
		)
		require.NoError(t, err)
		require.Equal(t, int64(1), count)
	})

	t.Run("moving an item out of one collection into another keeps both", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		userID := createCollectionUser(t, "collection-move@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/move",
		)

		first, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"Wishlist",
		)
		require.NoError(t, err)

		second, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"Recipes",
		)
		require.NoError(t, err)

		require.True(t, second.CollectionCreated)
		require.False(t, second.AlreadyInCollection)
		require.NotEqual(t, first.Collection.ID, second.Collection.ID)

		// The saved item now belongs to exactly one collection, the new one.
		require.Equal(t, second.Collection.ID, savedItemState(t, savedItemID).CollectionID)
		require.Equal(t, int64(0), countSavedItemsInCollection(t, first.Collection.ID))
		require.Equal(t, int64(1), countSavedItemsInCollection(t, second.Collection.ID))

		// The source collection is left alone. Cleaning up emptied user collections
		// is explicitly not part of this operation.
		_, err = collectiondbtest.New(testPool).GetCollectionState(
			context.Background(),
			first.Collection.ID,
		)
		require.NoError(t, err)
	})

	t.Run("two collections for one user do not collide across users", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		userA := createCollectionUser(t, "collection-scope-a@example.com")
		userB := createCollectionUser(t, "collection-scope-b@example.com")

		unsortedA := createUnsorted(t, userA)
		unsortedB := createUnsorted(t, userB)

		itemA := createSavedItemInCollection(
			t,
			userA,
			unsortedA,
			"https://example.com/scope-a",
		)
		itemB := createSavedItemInCollection(
			t,
			userB,
			unsortedB,
			"https://example.com/scope-b",
		)

		resultA, err := svc.PutSavedItem(
			context.Background(),
			userA,
			itemA,
			"Wishlist",
		)
		require.NoError(t, err)

		resultB, err := svc.PutSavedItem(
			context.Background(),
			userB,
			itemB,
			"Wishlist",
		)
		require.NoError(t, err)

		// The unique index is per user, so the same name is two collections.
		require.True(t, resultA.CollectionCreated)
		require.True(t, resultB.CollectionCreated)
		require.NotEqual(t, resultA.Collection.ID, resultB.Collection.ID)
		require.Equal(t, userA, resultA.Collection.UserID)
		require.Equal(t, userB, resultB.Collection.UserID)
	})

	t.Run("concurrent calls for the same name converge on one collection", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		ctx := context.Background()

		userID := createCollectionUser(t, "collection-concurrent@example.com")
		unsortedID := createUnsorted(t, userID)

		const callers = 4

		savedItemIDs := make([]uuid.UUID, 0, callers)

		for i := range callers {
			savedItemIDs = append(
				savedItemIDs,
				createSavedItemInCollection(
					t,
					userID,
					unsortedID,
					"https://example.com/concurrent/"+string(rune('a'+i)),
				),
			)
		}

		type outcome struct {
			collectionID        uuid.UUID
			collectionCreated   bool
			alreadyInCollection bool
			err                 error
		}

		results := make([]outcome, callers)

		var wg sync.WaitGroup

		for i := range callers {
			wg.Add(1)

			go func(index int) {
				defer wg.Done()

				result, err := svc.PutSavedItem(
					ctx,
					userID,
					savedItemIDs[index],
					"Wishlist",
				)

				results[index] = outcome{
					collectionID:        result.Collection.ID,
					collectionCreated:   result.CollectionCreated,
					alreadyInCollection: result.AlreadyInCollection,
					err:                 err,
				}
			}(i)
		}

		wg.Wait()

		// This is why the create-or-get is two statements rather than a single
		// data-modifying CTE: a CTE shares one snapshot with its own SELECT, so a
		// caller that lost the insert race would have seen no collection at all.
		expectedID := uuid.Nil

		createdCount := 0

		for i, res := range results {
			require.NoError(t, res.err)
			require.NotEqual(t, uuid.Nil, res.collectionID)

			if expectedID == uuid.Nil {
				expectedID = res.collectionID
			}

			require.Equal(
				t,
				expectedID,
				res.collectionID,
				"caller %d resolved a different collection", i,
			)

			if res.collectionCreated {
				createdCount++
			}

			require.Equal(
				t,
				res.collectionID,
				savedItemState(t, savedItemIDs[i]).CollectionID,
			)
		}

		require.Equal(
			t,
			1,
			createdCount,
			"exactly one caller may have created the collection",
		)

		// And the database agrees: exactly one row for that name.
		require.Equal(t, int64(1), countCollectionsNamed(t, userID, "Wishlist"))
	})

	t.Run("rejects a blank name before touching the database", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		userID := createCollectionUser(t, "collection-blank@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/blank",
		)

		before := savedItemState(t, savedItemID)

		_, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"   ",
		)
		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeInvalidCollectionName,
			apperror.FromError(err).Code,
		)

		// Nothing was written at all.
		require.Equal(t, int64(0), countCollectionsNamed(t, userID, ""))
		require.Equal(t, before.UpdatedAt, savedItemState(t, savedItemID).UpdatedAt)
	})

	t.Run("stores the trimmed name", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		userID := createCollectionUser(t, "collection-trimmed@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/trimmed",
		)

		result, err := svc.PutSavedItem(
			ctx,
			userID,
			savedItemID,
			"\t  Wishlist \n",
		)
		require.NoError(t, err)

		// The stored display name is trimmed but keeps its casing: it is a display
		// name, not a lookup key.
		require.Equal(t, "Wishlist", result.Collection.Name)

		stored, err := db.GetCollectionState(ctx, result.Collection.ID)
		require.NoError(t, err)
		require.Equal(t, "Wishlist", stored.Name)
	})

	t.Run("a long name is accepted and a too long name is not", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		userID := createCollectionUser(t, "collection-length@example.com")
		unsortedID := createUnsorted(t, userID)

		atLimitID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/at-limit",
		)
		overLimitID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/over-limit",
		)

		atLimit := strings.Repeat("a", collection.CollectionNameMaxLength)

		result, err := svc.PutSavedItem(
			context.Background(),
			userID,
			atLimitID,
			atLimit,
		)
		require.NoError(t, err)
		require.Equal(t, atLimit, result.Collection.Name)

		_, err = svc.PutSavedItem(
			context.Background(),
			userID,
			overLimitID,
			atLimit+"a",
		)
		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeInvalidCollectionName,
			apperror.FromError(err).Code,
		)
		require.Equal(t, int64(0), countCollectionsNamed(t, userID, atLimit+"a"))
	})

	t.Run("the returned collection carries no enrichment dependency", func(t *testing.T) {
		truncateCollectionData(t)

		svc := newCollectionService(t)

		userID := createCollectionUser(t, "collection-projection@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/projection",
		)

		result, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"Wishlist",
		)
		require.NoError(t, err)

		// The projection is the existing saved item projection plus collection_id,
		// so the rest of the item is unchanged by a move.
		require.Equal(t, "https://example.com/projection", result.SavedItem.URL)
		require.Equal(t, "example.com", result.SavedItem.Domain.String)
		require.True(t, result.SavedItem.Domain.Valid)
		require.False(t, result.SavedItem.Platform.Valid)
		require.False(t, result.SavedItem.Title.Valid)
		require.False(t, result.SavedItem.CreatedAt.Time.IsZero())
	})
}

// countSavedItemsInCollection counts the saved items filed under a collection,
// which lets a test tell "still there" from "emptied but not deleted".
func countSavedItemsInCollection(t *testing.T, collectionID uuid.UUID) int64 {
	t.Helper()

	var count int64

	err := testPool.QueryRow(
		context.Background(),
		"SELECT COUNT(*) FROM saved_items WHERE collection_id = $1",
		collectionID,
	).Scan(&count)
	require.NoError(t, err)

	return count
}

// timeAt returns a fixed offset from now, so enrichment timestamps in tests are
// clearly distinguishable from column defaults.
func timeAt(t *testing.T, offset time.Duration) time.Time {
	t.Helper()

	return time.Now().Add(offset).UTC().Truncate(time.Millisecond)
}
