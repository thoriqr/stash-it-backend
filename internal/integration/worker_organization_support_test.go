package integration_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/logger"
	workerorganizationdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/worker_organization/generated"
	workerorganization "github.com/thoriqr/stash-it-backend/internal/worker/organization"
	workerorganizationdb "github.com/thoriqr/stash-it-backend/internal/worker/organization/generated"
	"github.com/thoriqr/stash-it-backend/internal/worker/queue"
	"go.uber.org/zap"
)

// Support for the organization integration tests: the queue-facing helpers and
// the two small row helpers the tests in this file use.

// startTestOrganizationWorker runs a real Asynq server against the test Redis
// with the organization handler registered, and stops it when the test ends.
//
// It is built from the same package-level pieces the worker binary uses, so a
// change to the task type, the queue name or the handler has to be reflected here
// or these tests stop finding anything.
func startTestOrganizationWorker(t *testing.T) {
	t.Helper()

	log, err := logger.New("development")
	require.NoError(t, err)

	server := asynq.NewServerFromRedisClient(
		startRedisForTests(t),
		asynq.Config{
			Concurrency:     1,
			Queues:          map[string]int{queue.QueueOrganization: 1},
			RetryDelayFunc:  testRetryDelay,
			Logger:          logger.Asynq(log),
			ShutdownTimeout: 5 * time.Second,
		},
	)

	mux := asynq.NewServeMux()
	mux.Handle(
		queue.TaskTypeOrganizeSavedItem,
		workerorganization.NewHandler(
			workerorganization.NewService(
				workerorganization.NewRepository(
					testPool,
					workerorganizationdb.New(testPool),
				),
				log,
			),
			log,
		),
	)

	require.NoError(t, server.Start(mux))

	t.Cleanup(server.Shutdown)
}

// readOrganizationQueueState reports the organization queue's accounting, read
// through Asynq's own Inspector rather than by counting Redis keys.
func readOrganizationQueueState(t *testing.T) queueState {
	t.Helper()

	info, err := inspectorForQueue(t).
		GetQueueInfo(queue.QueueOrganization)
	if err != nil {
		// A queue that does not exist yet holds no tasks.
		return queueState{}
	}

	return queueState{
		pending:  info.Pending,
		active:   info.Active,
		retry:    info.Retry,
		archived: info.Archived,
	}
}

// organizationQueueIsEmpty reports that nothing is left on the organization
// queue.
func organizationQueueIsEmpty(t *testing.T) bool {
	t.Helper()

	state := readOrganizationQueueState(t)

	return state.pending == 0 &&
		state.active == 0 &&
		state.retry == 0 &&
		state.archived == 0
}

// flushOrganizationQueue empties the organization queue before a test, so one
// test's task is never consumed while another test asserts on its own item.
func flushOrganizationQueue(t *testing.T) {
	t.Helper()

	inspector := inspectorForQueue(t)

	queues, err := inspector.Queues()
	require.NoError(t, err)

	if !slices.Contains(queues, queue.QueueOrganization) {
		return
	}

	for _, deleteTasks := range []func(string) (int, error){
		inspector.DeleteAllPendingTasks,
		inspector.DeleteAllRetryTasks,
		inspector.DeleteAllScheduledTasks,
		inspector.DeleteAllArchivedTasks,
		inspector.DeleteAllCompletedTasks,
	} {
		_, err := deleteTasks(queue.QueueOrganization)
		require.NoError(t, err)
	}
}

// moveItemToCollection files an item into a collection the way the collection API
// does, and returns the item's updated_at so a test can assert nothing rewrote it
// afterwards.
func moveItemToCollection(
	t *testing.T,
	savedItemID uuid.UUID,
	userID uuid.UUID,
	collectionID uuid.UUID,
) pgtype.Timestamptz {
	t.Helper()

	require.NoError(
		t,
		workerorganizationdbtest.New(testPool).
			MoveSavedItemForWorkerOrganization(
				context.Background(),
				workerorganizationdbtest.MoveSavedItemForWorkerOrganizationParams{
					CollectionID: collectionID,
					ID:           savedItemID,
					UserID:       userID,
				},
			),
	)

	state, err := workerorganizationdbtest.New(testPool).
		GetWorkerOrganizationItemState(context.Background(), savedItemID)
	require.NoError(t, err)

	return state.UpdatedAt
}

// sameCollection reports whether the item is currently in the given collection.
func sameCollection(
	t *testing.T,
	savedItemID uuid.UUID,
	collectionID uuid.UUID,
) bool {
	t.Helper()

	state, err := workerorganizationdbtest.New(testPool).
		GetWorkerOrganizationItemState(context.Background(), savedItemID)
	require.NoError(t, err)

	return state.CollectionID == collectionID
}

// unsortedIDFor returns the user's Unsorted collection id.
func unsortedIDFor(t *testing.T, userID uuid.UUID) uuid.UUID {
	t.Helper()

	collections := organizationCollectionsForUser(t, userID)

	for _, collection := range collections {
		if collection.SystemKey.Valid &&
			collection.SystemKey.String == "unsorted" {
			return collection.ID
		}
	}

	require.Fail(t, "user has no unsorted collection")

	return uuid.Nil
}

// enrichmentcoreMetadata builds extractor metadata carrying one platform.
func enrichmentcoreMetadata(platform string) enrichmentcore.Metadata {
	return enrichmentcore.Metadata{
		Title:    textPtr("An interesting article"),
		Platform: textPtr(platform),
	}
}

func textPtr(value string) *string {
	return &value
}

// testLogger builds the same development logger the worker binary uses.
func testLogger(t *testing.T) *zap.Logger {
	t.Helper()

	log, err := logger.New("development")
	require.NoError(t, err)

	return log
}
