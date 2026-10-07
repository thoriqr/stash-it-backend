package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	workerorganizationdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/worker_organization/generated"
	workerorganization "github.com/thoriqr/stash-it-backend/internal/worker/organization"
	workerorganizationdb "github.com/thoriqr/stash-it-backend/internal/worker/organization/generated"
	"github.com/thoriqr/stash-it-backend/internal/worker/queue"
)

// These tests drive the organization worker against a real database: the worker's
// own repository, its worker-scoped sqlc target, the baseline schema with
// migration 000025's composite ownership constraint, and the real statements.
//
// The decision under test is one the repository has to make against a locked row,
// so almost nothing here is meaningful with a mock: which collection the item
// ends up in, whether a second collection appears, and whether a user action that
// happened while the task waited is respected can only be observed against real
// rows.

func truncateWorkerOrganizationData(t *testing.T) {
	t.Helper()

	require.NoError(
		t,
		workerorganizationdbtest.New(testPool).
			TruncateWorkerOrganizationData(context.Background()),
	)
}

func createOrganizationUser(t *testing.T, email string) uuid.UUID {
	t.Helper()

	userID, err := workerorganizationdbtest.New(testPool).
		CreateWorkerOrganizationUser(
			context.Background(),
			workerorganizationdbtest.CreateWorkerOrganizationUserParams{
				Email:       email,
				DisplayName: "Worker Organization Test User",
			},
		)

	require.NoError(t, err)

	return userID
}

func createOrganizationUnsorted(t *testing.T, userID uuid.UUID) uuid.UUID {
	t.Helper()

	collectionID, err := workerorganizationdbtest.New(testPool).
		CreateUnsortedCollectionForWorkerOrganization(
			context.Background(),
			userID,
		)

	require.NoError(t, err)

	return collectionID
}

func createUserNamedCollection(
	t *testing.T,
	userID uuid.UUID,
	name string,
) uuid.UUID {
	t.Helper()

	collectionID, err := workerorganizationdbtest.New(testPool).
		CreateUserNamedCollectionForWorkerOrganization(
			context.Background(),
			workerorganizationdbtest.CreateUserNamedCollectionForWorkerOrganizationParams{
				UserID: userID,
				Name:   name,
			},
		)

	require.NoError(t, err)

	return collectionID
}

func createOrganizationSavedItem(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
	platform *string,
	enrichmentStatus string,
) uuid.UUID {
	t.Helper()

	savedItemID, err := workerorganizationdbtest.New(testPool).
		CreateWorkerOrganizationSavedItem(
			context.Background(),
			workerorganizationdbtest.CreateWorkerOrganizationSavedItemParams{
				UserID:           userID,
				Url:              "https://example.com/articles/1",
				Domain:           testText("example.com"),
				Platform:         organizationTestText(platform),
				EnrichmentStatus: enrichmentStatus,
				CollectionID:     collectionID,
			},
		)

	require.NoError(t, err)

	return savedItemID
}

func organizationTestText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}

	return pgtype.Text{String: *value, Valid: true}
}

func organizationItemState(
	t *testing.T,
	savedItemID uuid.UUID,
) workerorganizationdbtest.GetWorkerOrganizationItemStateRow {
	t.Helper()

	state, err := workerorganizationdbtest.New(testPool).
		GetWorkerOrganizationItemState(context.Background(), savedItemID)

	require.NoError(t, err)

	return state
}

func organizationCollection(
	t *testing.T,
	collectionID uuid.UUID,
) workerorganizationdbtest.GetWorkerOrganizationCollectionRow {
	t.Helper()

	collection, err := workerorganizationdbtest.New(testPool).
		GetWorkerOrganizationCollection(context.Background(), collectionID)

	require.NoError(t, err)

	return collection
}

func organizationCollectionsForUser(
	t *testing.T,
	userID uuid.UUID,
) []workerorganizationdbtest.ListWorkerOrganizationCollectionsForUserRow {
	t.Helper()

	collections, err := workerorganizationdbtest.New(testPool).
		ListWorkerOrganizationCollectionsForUser(
			context.Background(),
			userID,
		)

	require.NoError(t, err)

	return collections
}

// newRealOrganizationService builds the worker service against the shared test
// pool, through the real repository.
func newRealOrganizationService(t *testing.T) workerorganization.Service {
	t.Helper()

	return workerorganization.NewService(
		workerorganization.NewRepository(
			testPool,
			workerorganizationdb.New(testPool),
		),
		testLogger(t),
	)
}

func organizeNow(
	t *testing.T,
	savedItemID uuid.UUID,
	userID uuid.UUID,
) error {
	t.Helper()

	return newRealOrganizationService(t).
		OrganizeSavedItem(context.Background(), savedItemID, userID)
}

// An item still in Unsorted with a completed enrichment and a platform is filed
// into a collection named by that platform, and the collection is created because
// no such collection existed.
func TestOrganization_FilesItemIntoANewSystemCollection(t *testing.T) {
	truncateWorkerOrganizationData(t)

	userID := createOrganizationUser(t, "org-new@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	platform := "YouTube"

	savedItemID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&platform,
		"completed",
	)

	require.NoError(t, organizeNow(t, savedItemID, userID))

	state := organizationItemState(t, savedItemID)

	require.NotEqual(
		t,
		unsortedID,
		state.CollectionID,
		"the item must leave Unsorted",
	)

	collection := organizationCollection(t, state.CollectionID)

	require.Equal(t, userID, collection.UserID)
	require.Equal(t, collection.Name, "YouTube")
	require.Equal(t, collection.Type, "system")
}

// The system key is the platform's normalized identity, derived the same way the
// name index compares names.
func TestOrganization_CreatedCollectionHasTheNormalizedSystemKey(t *testing.T) {
	truncateWorkerOrganizationData(t)

	userID := createOrganizationUser(t, "org-key@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	platform := "  YouTube  "

	savedItemID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&platform,
		"completed",
	)

	require.NoError(t, organizeNow(t, savedItemID, userID))

	collection := organizationCollection(
		t,
		organizationItemState(t, savedItemID).CollectionID,
	)

	require.Equal(t, collection.Name, "YouTube")
	require.Equal(t, collection.SystemKey.String, "youtube")
	require.True(t, collection.SystemKey.Valid)
}

// A collection the user named themselves is the target when it matches the
// platform. The user's naming is theirs, so nothing is created beside it and
// nothing is renamed.
func TestOrganization_ReusesAMatchingUserCollection(t *testing.T) {
	truncateWorkerOrganizationData(t)

	userID := createOrganizationUser(t, "org-reuse-user@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)
	existingID := createUserNamedCollection(t, userID, "youtube")

	platform := "YouTube"

	savedItemID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&platform,
		"completed",
	)

	require.NoError(t, organizeNow(t, savedItemID, userID))

	require.Equal(
		t,
		existingID,
		organizationItemState(t, savedItemID).CollectionID,
		"a user-named collection matching the platform is the target",
	)

	collection := organizationCollection(t, existingID)

	require.Equal(t, collection.Name, "youtube", "the user's own name is kept")
	require.Equal(t, collection.Type, "user")

	// Exactly two collections exist: Unsorted and the user's own. A second
	// "YouTube" would show up here.
	require.Len(
		t,
		organizationCollectionsForUser(t, userID),
		2,
		"organization must not create a collection beside a matching one",
	)
}

// An automatically created platform collection is a valid target too. Matching it
// is what stops a second item from the same platform creating a second
// collection.
func TestOrganization_ReusesAMatchingSystemCollection(t *testing.T) {
	truncateWorkerOrganizationData(t)

	userID := createOrganizationUser(t, "org-reuse-system@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	platform := "YouTube"

	firstID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&platform,
		"completed",
	)

	require.NoError(t, organizeNow(t, firstID, userID))

	platformCollectionID := organizationItemState(t, firstID).CollectionID

	// A second item, still in Unsorted, from the same platform.
	secondID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&platform,
		"completed",
	)

	require.NoError(t, organizeNow(t, secondID, userID))

	require.Equal(
		t,
		platformCollectionID,
		organizationItemState(t, secondID).CollectionID,
		"the second item joins the collection the first one created",
	)

	require.Len(
		t,
		organizationCollectionsForUser(t, userID),
		2,
		"one Unsorted and one platform collection, not two platform collections",
	)
}

// Case is not a distinction the product makes: the same platform saved from two
// pages that spell it differently lands in one collection.
func TestOrganization_MatchesTheNameCaseInsensitively(t *testing.T) {
	truncateWorkerOrganizationData(t)

	userID := createOrganizationUser(t, "org-case@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	upper := "YouTube"
	lower := "youtube"

	firstID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&upper,
		"completed",
	)

	require.NoError(t, organizeNow(t, firstID, userID))

	secondID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&lower,
		"completed",
	)

	require.NoError(t, organizeNow(t, secondID, userID))

	require.Equal(
		t,
		organizationItemState(t, firstID).CollectionID,
		organizationItemState(t, secondID).CollectionID,
	)

	require.Len(t, organizationCollectionsForUser(t, userID), 2)
}

// The item is left exactly where the user put it. This is the case the whole
// guard exists for: the task was queued when the item was in Unsorted, and the
// user acted before the worker ran.
func TestOrganization_DoesNotMoveAnItemTheUserFiledElsewhere(t *testing.T) {
	truncateWorkerOrganizationData(t)

	userID := createOrganizationUser(t, "org-user-moved@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)
	favoritesID := createUserNamedCollection(t, userID, "Favorites")

	platform := "YouTube"

	savedItemID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&platform,
		"completed",
	)

	// The user files the item while the organization task is still waiting.
	movedAt := moveItemToCollection(
		t,
		savedItemID,
		userID,
		favoritesID,
	)

	require.NoError(t, organizeNow(t, savedItemID, userID))

	state := organizationItemState(t, savedItemID)

	require.Equal(
		t,
		favoritesID,
		state.CollectionID,
		"the user's own filing must win over automatic organization",
	)

	// And the user's action must not have been rewritten afterwards.
	require.Equal(
		t,
		movedAt.Time,
		state.UpdatedAt.Time,
		"organization must not rewrite an item it left alone",
	)

	// No platform collection was created for an item nobody asked to move.
	require.Len(t, organizationCollectionsForUser(t, userID), 2)
}

// An enrichment that has not succeeded leaves nothing to organize on. The
// platform check alone would not catch this: a failed enrichment preserves the
// metadata the item already had, so a platform can be present on an item whose
// latest enrichment failed.
func TestOrganization_SkipsAnItemWhoseEnrichmentDidNotComplete(t *testing.T) {
	truncateWorkerOrganizationData(t)

	userID := createOrganizationUser(t, "org-enrichment-failed@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	platform := "YouTube"

	savedItemID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&platform,
		"failed",
	)

	require.NoError(t, organizeNow(t, savedItemID, userID))

	require.Equal(
		t,
		unsortedID,
		organizationItemState(t, savedItemID).CollectionID,
	)

	require.Len(
		t,
		organizationCollectionsForUser(t, userID),
		1,
		"nothing is created for an item whose enrichment did not complete",
	)
}

// A page that exposed no platform has nothing to organize on.
func TestOrganization_SkipsAnItemWithNoPlatform(t *testing.T) {
	truncateWorkerOrganizationData(t)

	userID := createOrganizationUser(t, "org-no-platform@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	savedItemID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		nil,
		"completed",
	)

	require.NoError(t, organizeNow(t, savedItemID, userID))

	require.Equal(
		t,
		unsortedID,
		organizationItemState(t, savedItemID).CollectionID,
	)

	require.Len(t, organizationCollectionsForUser(t, userID), 1)
}

// A deleted item is a finished task, and no outbound or collection work happens
// for a row that is gone.
func TestOrganization_MissingItemIsDiscarded(t *testing.T) {
	truncateWorkerOrganizationData(t)

	userID := createOrganizationUser(t, "org-deleted@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	platform := "YouTube"

	savedItemID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&platform,
		"completed",
	)

	require.NoError(
		t,
		workerorganizationdbtest.New(testPool).
			DeleteSavedItemForWorkerOrganization(
				context.Background(),
				savedItemID,
			),
	)

	err := organizeNow(t, savedItemID, userID)

	require.ErrorIs(t, err, workerorganization.ErrSavedItemNotFound)

	require.Len(
		t,
		organizationCollectionsForUser(t, userID),
		1,
		"a deleted item must not create a collection",
	)
}

// A task naming somebody else's item is refused and the item is left alone. It
// cannot happen through any path the product has, which is why it is reported
// rather than quietly discarded.
func TestOrganization_OwnershipMismatchIsRefused(t *testing.T) {
	truncateWorkerOrganizationData(t)

	ownerID := createOrganizationUser(t, "org-owner@example.com")
	otherID := createOrganizationUser(t, "org-other@example.com")

	ownerUnsorted := createOrganizationUnsorted(t, ownerID)
	createOrganizationUnsorted(t, otherID)

	platform := "YouTube"

	savedItemID := createOrganizationSavedItem(
		t,
		ownerID,
		ownerUnsorted,
		&platform,
		"completed",
	)

	before := organizationItemState(t, savedItemID)

	err := organizeNow(t, savedItemID, otherID)

	require.ErrorIs(t, err, workerorganization.ErrSavedItemOwnershipMismatch)

	after := organizationItemState(t, savedItemID)

	require.Equal(
		t,
		before.CollectionID,
		after.CollectionID,
		"an item that is not the task's to touch must not be moved",
	)
	require.Equal(
		t,
		before.UpdatedAt,
		after.UpdatedAt,
		"and must not be written at all",
	)

	require.Len(
		t,
		organizationCollectionsForUser(t, otherID),
		1,
		"the other user must not gain a collection",
	)
}

// A delivered task may run more than once. The second run finds the item already
// filed, creates nothing, and does not touch updated_at.
func TestOrganization_RepeatedDeliveryIsIdempotent(t *testing.T) {
	truncateWorkerOrganizationData(t)

	userID := createOrganizationUser(t, "org-idempotent@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	platform := "YouTube"

	savedItemID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&platform,
		"completed",
	)

	require.NoError(t, organizeNow(t, savedItemID, userID))

	afterFirst := organizationItemState(t, savedItemID)

	require.NotEqual(t, unsortedID, afterFirst.CollectionID)

	for range 3 {
		require.NoError(t, organizeNow(t, savedItemID, userID))
	}

	afterRepeated := organizationItemState(t, savedItemID)

	require.Equal(
		t,
		afterFirst.CollectionID,
		afterRepeated.CollectionID,
	)
	require.Equal(
		t,
		afterFirst.UpdatedAt,
		afterRepeated.UpdatedAt,
		"an already-organized item must not be rewritten",
	)

	require.Len(
		t,
		organizationCollectionsForUser(t, userID),
		2,
		"repeated delivery must not create another collection",
	)
}

// Organization never touches what enrichment recorded, in either direction.
func TestOrganization_LeavesEnrichmentStateUntouched(t *testing.T) {
	truncateWorkerOrganizationData(t)

	userID := createOrganizationUser(t, "org-enrichment-state@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	platform := "YouTube"

	savedItemID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&platform,
		"completed",
	)

	before := organizationItemState(t, savedItemID)

	require.NoError(t, organizeNow(t, savedItemID, userID))

	after := organizationItemState(t, savedItemID)

	require.Equal(t, before.EnrichmentStatus, after.EnrichmentStatus)
	require.Equal(t, "completed", after.EnrichmentStatus)
	require.Equal(t, before.LastEnrichedAt.Time, after.LastEnrichedAt.Time)
	require.Equal(t, before.Platform.String, after.Platform.String)
}

// A database failure leaves the item exactly as it was and is reported so the
// queue can retry it. The item stays a completed, valid saved item.
func TestOrganization_DatabaseFailureLeavesEnrichmentCompleted(t *testing.T) {
	truncateWorkerOrganizationData(t)

	userID := createOrganizationUser(t, "org-db-failure@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	platform := "YouTube"

	savedItemID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&platform,
		"completed",
	)

	before := organizationItemState(t, savedItemID)

	// A cancelled context is a database failure from the repository's point of
	// view: the transaction cannot begin, so nothing is written.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := newRealOrganizationService(t).
		OrganizeSavedItem(ctx, savedItemID, userID)

	require.Error(t, err)

	after := organizationItemState(t, savedItemID)

	require.Equal(t, "completed", after.EnrichmentStatus)
	require.Equal(t, before.CollectionID, after.CollectionID)
	require.Equal(t, before.UpdatedAt, after.UpdatedAt)
}

// Concurrent organization for the same user and platform must not produce two
// collections. The unique index is what decides it, not a check the application
// made first.
func TestOrganization_ConcurrentTasksDoNotDuplicateCollections(t *testing.T) {
	truncateWorkerOrganizationData(t)

	userID := createOrganizationUser(t, "org-concurrent@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	platform := "YouTube"

	const itemCount = 6

	itemIDs := make([]uuid.UUID, 0, itemCount)

	for range itemCount {
		itemIDs = append(
			itemIDs,
			createOrganizationSavedItem(
				t,
				userID,
				unsortedID,
				&platform,
				"completed",
			),
		)
	}

	// Every task runs at once against the same user, so the collection they all
	// want is contended from the first insert onwards.
	errs := make(chan error, itemCount)

	for _, savedItemID := range itemIDs {
		go func(savedItemID uuid.UUID) {
			errs <- organizeNow(t, savedItemID, userID)
		}(savedItemID)
	}

	for range itemCount {
		require.NoError(t, <-errs)
	}

	collections := organizationCollectionsForUser(t, userID)

	// Unsorted plus exactly one platform collection.
	require.Len(
		t,
		collections,
		2,
		"concurrent organization must produce exactly one platform collection",
	)

	platformCollections := 0

	for _, collection := range collections {
		if collection.Type == "system" && collection.Name != "Unsorted" {
			platformCollections++

			require.Equal(t, collection.Name, "YouTube")
			require.Equal(t, collection.SystemKey.String, "youtube")
		}
	}

	require.Equal(t, 1, platformCollections)

	// Every item landed in that one collection.
	target := collections[1].ID

	for _, savedItemID := range itemIDs {
		require.Equal(
			t,
			target,
			organizationItemState(t, savedItemID).CollectionID,
		)
	}
}

// A platform collection belongs to one user, and migration 000025's composite
// ownership constraint is what makes that a database fact rather than a
// convention.
func TestOrganization_PlatformCollectionsAreNeverSharedAcrossUsers(t *testing.T) {
	truncateWorkerOrganizationData(t)

	firstUser := createOrganizationUser(t, "org-owner-a@example.com")
	secondUser := createOrganizationUser(t, "org-owner-b@example.com")

	firstUnsorted := createOrganizationUnsorted(t, firstUser)
	secondUnsorted := createOrganizationUnsorted(t, secondUser)

	platform := "YouTube"

	firstItem := createOrganizationSavedItem(
		t,
		firstUser,
		firstUnsorted,
		&platform,
		"completed",
	)
	secondItem := createOrganizationSavedItem(
		t,
		secondUser,
		secondUnsorted,
		&platform,
		"completed",
	)

	require.NoError(t, organizeNow(t, firstItem, firstUser))
	require.NoError(t, organizeNow(t, secondItem, secondUser))

	firstCollection := organizationItemState(t, firstItem).CollectionID
	secondCollection := organizationItemState(t, secondItem).CollectionID

	require.NotEqual(
		t,
		firstCollection,
		secondCollection,
		"each user gets their own platform collection",
	)

	require.Equal(
		t,
		firstUser,
		organizationCollection(t, firstCollection).UserID,
	)
	require.Equal(
		t,
		secondUser,
		organizationCollection(t, secondCollection).UserID,
	)

	require.Len(t, organizationCollectionsForUser(t, firstUser), 2)
	require.Len(t, organizationCollectionsForUser(t, secondUser), 2)
}

// A platform spelled like the reserved collection cannot become a target:
// filing an item into Unsorted would move it nowhere and report success.
func TestOrganization_ReservedNameIsRefused(t *testing.T) {
	truncateWorkerOrganizationData(t)

	userID := createOrganizationUser(t, "org-reserved@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	platform := "Unsorted"

	savedItemID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&platform,
		"completed",
	)

	err := organizeNow(t, savedItemID, userID)

	require.ErrorIs(
		t,
		err,
		workerorganization.ErrCollectionNameTakenByReservedName,
	)

	require.Len(
		t,
		organizationCollectionsForUser(t, userID),
		1,
		"no collection is created for a reserved name",
	)
}

// The whole flow, through the real queue: a save is enriched, the enrichment
// worker schedules organization, and the organization worker files the item.
//
// The enrichment and organization handlers run in one server here because that is
// what makes the handoff observable: the task the enrichment worker writes is the
// task the organization worker consumes.
func TestOrganization_EnrichmentProducesOrganizationThatFiles(t *testing.T) {
	truncateWorkerOrganizationData(t)
	flushEnrichmentQueue(t)
	flushOrganizationQueue(t)

	platform := "YouTube"

	enricher := &testutil.FakeEnricher{
		Metadata: enrichmentcoreMetadata(platform),
	}

	startTestWorkerWithRetryDelayAndOrganizer(
		t,
		enricher,
		queue.EnrichmentRetryDelay,
		queue.NewProducer(startRedisForTests(t)),
	)
	startTestOrganizationWorker(t)

	userID := createOrganizationUser(t, "org-end-to-end@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		unsortedID,
		"https://example.com/articles/end-to-end",
	)

	producer := queue.NewProducer(startRedisForTests(t))

	require.NoError(
		t,
		producer.EnqueueSavedItemEnrichment(
			context.Background(),
			savedItemID,
		),
	)

	enriched := awaitEnrichmentStatus(t, savedItemID, "completed")

	require.Equal(
		t,
		platform,
		enriched.Platform.String,
		"enrichment must record the platform it extracted",
	)

	// The organization worker has filed the item by the time the queue drains.
	require.Eventually(
		t,
		func() bool {
			return !sameCollection(t, savedItemID, unsortedID)
		},
		30*time.Second,
		50*time.Millisecond,
		"the item must leave Unsorted once organization has run",
	)

	state, err := workerorganizationdbtest.New(testPool).
		GetWorkerOrganizationItemState(context.Background(), savedItemID)
	require.NoError(t, err)

	require.Equal(t, "completed", state.EnrichmentStatus)

	collection := organizationCollection(t, state.CollectionID)

	require.Equal(t, collection.Name, platform)
	require.Equal(t, collection.Type, "system")
	require.Equal(t, collection.SystemKey.String, "youtube")

	// One task for enrichment, one for organization, both drained.
	require.Eventually(
		t,
		func() bool {
			return organizationQueueIsEmpty(t)
		},
		30*time.Second,
		50*time.Millisecond,
	)
}

// A failed enrichment schedules no organization task, and the item keeps its
// failed enrichment.
func TestOrganization_FailedEnrichmentSchedulesNothing(t *testing.T) {
	truncateWorkerOrganizationData(t)
	flushEnrichmentQueue(t)
	flushOrganizationQueue(t)

	enricher := &failOnceEnricher{
		permanent: true,
		failure: &enrichmentcore.Failure{
			Kind: enrichmentcore.FailureFetch,
			Err:  errors.New("dial tcp: i/o timeout"),
		},
	}

	startTestWorkerWithRetryDelay(t, enricher, testRetryDelay)
	startTestOrganizationWorker(t)

	userID := createOrganizationUser(t, "org-enrich-failed@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		unsortedID,
		"https://example.com/articles/unreachable",
	)

	producer := queue.NewProducer(startRedisForTests(t))

	require.NoError(
		t,
		producer.EnqueueSavedItemEnrichment(
			context.Background(),
			savedItemID,
		),
	)

	awaitEnrichmentStatus(t, savedItemID, "failed")

	// The organization queue stays empty: no task was ever created for it.
	require.Eventually(
		t,
		func() bool {
			return organizationQueueIsEmpty(t)
		},
		5*time.Second,
		50*time.Millisecond,
		"a failed enrichment must not schedule organization",
	)

	failedState, err := workerorganizationdbtest.New(testPool).
		GetWorkerOrganizationItemState(context.Background(), savedItemID)
	require.NoError(t, err)

	require.Equal(t, "failed", failedState.EnrichmentStatus)
	require.Equal(t, unsortedID, failedState.CollectionID)
}

// A completed enrichment with no platform schedules nothing either, and the item
// stays in Unsorted as a completed, valid saved item.
func TestOrganization_NoPlatformSchedulesNothing(t *testing.T) {
	truncateWorkerOrganizationData(t)
	flushEnrichmentQueue(t)
	flushOrganizationQueue(t)

	// A page that exposed nothing.
	startTestWorkerWithRetryDelay(t, &testutil.FakeEnricher{}, testRetryDelay)
	startTestOrganizationWorker(t)

	userID := createOrganizationUser(t, "org-enrich-silent@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		unsortedID,
		"https://example.com/articles/silent",
	)

	producer := queue.NewProducer(startRedisForTests(t))

	require.NoError(
		t,
		producer.EnqueueSavedItemEnrichment(
			context.Background(),
			savedItemID,
		),
	)

	awaitEnrichmentStatus(t, savedItemID, "completed")

	silentState, err := workerorganizationdbtest.New(testPool).
		GetWorkerOrganizationItemState(context.Background(), savedItemID)
	require.NoError(t, err)

	require.False(
		t,
		silentState.Platform.Valid,
		"a page that exposed nothing has no platform",
	)
	require.Equal(
		t,
		unsortedID,
		silentState.CollectionID,
		"and therefore stays where it is",
	)

	require.Len(t, organizationCollectionsForUser(t, userID), 1)
}

// A task for a deleted item finishes rather than being retried, and no collection
// is created for a row that is gone.
func TestOrganization_DeletedItemTaskIsDiscarded(t *testing.T) {
	truncateWorkerOrganizationData(t)
	flushOrganizationQueue(t)

	startTestOrganizationWorker(t)

	userID := createOrganizationUser(t, "org-task-deleted@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)

	platform := "YouTube"

	savedItemID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&platform,
		"completed",
	)

	producer := queue.NewProducer(startRedisForTests(t))

	require.NoError(
		t,
		producer.EnqueueSavedItemOrganization(
			context.Background(),
			savedItemID,
			userID,
		),
	)

	require.NoError(
		t,
		workerorganizationdbtest.New(testPool).
			DeleteSavedItemForWorkerOrganization(
				context.Background(),
				savedItemID,
			),
	)

	require.Eventually(
		t,
		func() bool {
			return organizationQueueIsEmpty(t)
		},
		30*time.Second,
		50*time.Millisecond,
		"a task for a deleted item must finish rather than be retried",
	)

	require.Len(
		t,
		organizationCollectionsForUser(t, userID),
		1,
		"a deleted item must not create a collection",
	)
}

// An ownership mismatch through the real queue is terminal, and it never touches
// the item.
func TestOrganization_OwnershipMismatchTaskIsDiscarded(t *testing.T) {
	truncateWorkerOrganizationData(t)
	flushOrganizationQueue(t)

	startTestOrganizationWorker(t)

	ownerID := createOrganizationUser(t, "org-task-owner@example.com")
	otherID := createOrganizationUser(t, "org-task-other@example.com")

	ownerUnsorted := createOrganizationUnsorted(t, ownerID)
	createOrganizationUnsorted(t, otherID)

	platform := "YouTube"

	savedItemID := createOrganizationSavedItem(
		t,
		ownerID,
		ownerUnsorted,
		&platform,
		"completed",
	)

	before := organizationItemState(t, savedItemID)

	producer := queue.NewProducer(startRedisForTests(t))

	require.NoError(
		t,
		producer.EnqueueSavedItemOrganization(
			context.Background(),
			savedItemID,
			otherID,
		),
	)

	require.Eventually(
		t,
		func() bool {
			state := readOrganizationQueueState(t)

			// Archived, not retried: the task stopped rather than being discarded.
			return state.pending == 0 && state.active == 0 && state.archived == 1
		},
		30*time.Second,
		50*time.Millisecond,
		"an ownership mismatch must stop the task",
	)

	after := organizationItemState(t, savedItemID)

	require.Equal(t, before.CollectionID, after.CollectionID)
	require.Equal(t, before.UpdatedAt, after.UpdatedAt)
	require.Len(t, organizationCollectionsForUser(t, otherID), 1)
}

// The boundary that matters most: the item is enriched, the task is queued, the
// user moves it, and the worker then runs. It must leave the item where the user
// put it.
func TestOrganization_DoesNotUndoAUserMoveMadeBeforeTheTaskRan(t *testing.T) {
	truncateWorkerOrganizationData(t)
	flushOrganizationQueue(t)

	startTestOrganizationWorker(t)

	userID := createOrganizationUser(t, "org-boundary@example.com")
	unsortedID := createOrganizationUnsorted(t, userID)
	favoritesID := createUserNamedCollection(t, userID, "Favorites")

	platform := "YouTube"

	savedItemID := createOrganizationSavedItem(
		t,
		userID,
		unsortedID,
		&platform,
		"completed",
	)

	producer := queue.NewProducer(startRedisForTests(t))

	require.NoError(
		t,
		producer.EnqueueSavedItemOrganization(
			context.Background(),
			savedItemID,
			userID,
		),
	)

	// The user files the item before the worker picks the task up. Nothing is
	// consuming the queue yet, so this is the item exactly as it was when the task
	// was written.
	moveItemToCollection(t, savedItemID, userID, favoritesID)

	// The worker starts and drains the task.
	require.Eventually(
		t,
		func() bool {
			return organizationQueueIsEmpty(t)
		},
		30*time.Second,
		50*time.Millisecond,
		"the organization task must run",
	)

	state := organizationItemState(t, savedItemID)

	require.Equal(
		t,
		favoritesID,
		state.CollectionID,
		"the task must not move an item the user filed after it was queued",
	)

	require.Len(
		t,
		organizationCollectionsForUser(t, userID),
		2,
		"and must not create a platform collection for it",
	)
}
