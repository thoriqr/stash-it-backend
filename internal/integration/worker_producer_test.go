package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/testutil"
	"github.com/thoriqr/stash-it-backend/internal/worker/queue"
)

// These tests drive the real Producer against the test Redis and read the task
// back out of it.
//
// Asserting on the constants would only prove they exist, not that they reach
// Asynq. The contract between the API and the worker is carried entirely by what
// the producer writes: the type Asynq matches a handler by, the queue the worker
// listens to, and the bounds on one attempt and on the retries of it. A producer
// that quietly dropped any of those would still pass a test that only read the
// package's own values, and would fail as "the worker looks healthy and idle" in
// production instead.
//
// No worker is started here on purpose. Without a consumer, a task stays pending,
// so the assertion is about what was written rather than about a race with one.

// awaitPendingEnrichmentTask waits for exactly one task to be pending on the
// enrichment queue and returns it.
func awaitPendingEnrichmentTask(t *testing.T) *asynq.TaskInfo {
	t.Helper()

	return awaitPendingEnrichmentTasks(t, 1)[0]
}

// awaitPendingEnrichmentTasks waits for the expected number of tasks to be
// pending and returns them.
//
// Pending is the state being asserted because nothing consumes them in this
// file. Enqueue is one Redis round trip, so a task is pending almost immediately;
// the wait is for the write to be visible rather than for a worker to run.
func awaitPendingEnrichmentTasks(
	t *testing.T,
	expected int,
) []*asynq.TaskInfo {
	t.Helper()

	var last []*asynq.TaskInfo

	require.Eventually(
		t,
		func() bool {
			tasks, err := inspectorForQueue(t).
				ListPendingTasks(queue.QueueEnrichment)
			require.NoError(t, err)

			last = tasks

			return len(tasks) == expected
		},
		10*time.Second,
		25*time.Millisecond,
		"expected %d pending enrichment task(s), last saw %d",
		expected,
		len(last),
	)

	return last
}

// The task the producer writes carries the whole contract: the type the worker
// registers a handler for, the queue it lands on, and the bounds on a single
// attempt and on the retries of it.
func TestProducer_EnqueuesEnrichmentTaskWithItsBounds(t *testing.T) {
	flushEnrichmentQueue(t)

	producer := queue.NewProducer(startRedisForTests(t))

	savedItemID := uuid.New()

	require.NoError(
		t,
		producer.EnqueueSavedItemEnrichment(
			context.Background(),
			savedItemID,
		),
	)

	info := awaitPendingEnrichmentTask(t)

	require.Equal(
		t,
		queue.TaskTypeEnrichSavedItem,
		info.Type,
		"the task type must be the one the worker registers a handler for",
	)
	require.Equal(
		t,
		queue.QueueEnrichment,
		info.Queue,
		"the task must land on the queue the worker listens to",
	)
	require.Equal(
		t,
		queue.EnrichmentTaskTimeout,
		info.Timeout,
		"the per-attempt timeout must be the configured one",
	)
	require.Equal(
		t,
		queue.EnrichmentMaxRetry,
		info.MaxRetry,
		"the retry budget must be the configured one",
	)
	require.Zero(
		t,
		info.Retried,
		"a freshly enqueued task has not been retried yet",
	)

	// The payload is the saved item's id and nothing else, so the worker reads
	// the URL from the row rather than from a second copy of it.
	var payload queue.EnrichSavedItemPayload

	require.NoError(t, json.Unmarshal([]byte(info.Payload), &payload))
	require.Equal(t, savedItemID, payload.SavedItemID)
}

// The producer deduplicates nothing, so two saves queue two tasks. Enrichment is
// repeatable and a user is allowed to ask again, and collapsing the two would
// make the second request silently depend on the first one's timing.
func TestProducer_DoesNotDeduplicateRepeatedTasks(t *testing.T) {
	flushEnrichmentQueue(t)

	producer := queue.NewProducer(startRedisForTests(t))

	savedItemID := uuid.New()

	for range 2 {
		require.NoError(
			t,
			producer.EnqueueSavedItemEnrichment(
				context.Background(),
				savedItemID,
			),
		)
	}

	tasks := awaitPendingEnrichmentTasks(t, 2)

	ids := make(map[string]struct{}, len(tasks))

	for _, task := range tasks {
		ids[task.ID] = struct{}{}
	}

	require.Len(
		t,
		ids,
		2,
		"each save must produce its own task",
	)
}

// An unreachable queue must be reported as an error rather than swallowed. The
// save flow decides what to do about it, and it can only log a real failure if
// the producer told it there was one.
func TestProducer_ReportsAnUnreachableQueue(t *testing.T) {
	producer := queue.NewProducer(unreachableRedisForTests(t))

	err := producer.EnqueueSavedItemEnrichment(
		context.Background(),
		uuid.New(),
	)

	require.Error(
		t,
		err,
		"an unreachable queue must be reported, not swallowed by the producer",
	)
}

// An organization task carries both ids on its own queue, with its own bounds,
// because the contract between the enrichment worker and the organization worker
// is a payload and a queue name.
func TestProducer_EnqueuesOrganizationTaskWithItsBounds(t *testing.T) {
	flushOrganizationQueue(t)

	producer := queue.NewProducer(startRedisForTests(t))

	savedItemID := uuid.New()
	userID := uuid.New()

	require.NoError(
		t,
		producer.EnqueueSavedItemOrganization(
			context.Background(),
			savedItemID,
			userID,
		),
	)

	info, err := inspectorForQueue(t).
		GetTaskInfo(queue.QueueOrganization, pendingOrganizationTaskID(t))
	require.NoError(t, err)

	require.Equal(
		t,
		queue.TaskTypeOrganizeSavedItem,
		info.Type,
		"the task type must be the one the worker registers a handler for",
	)
	require.Equal(
		t,
		queue.QueueOrganization,
		info.Queue,
		"the task must land on the organization queue, not the enrichment one",
	)
	require.Equal(
		t,
		queue.OrganizationTaskTimeout,
		info.Timeout,
	)
	require.Equal(t, queue.OrganizationMaxRetry, info.MaxRetry)
	require.Zero(t, info.Retried)

	var payload queue.OrganizeSavedItemPayload

	require.NoError(t, json.Unmarshal([]byte(info.Payload), &payload))
	require.Equal(t, savedItemID, payload.SavedItemID)
	require.Equal(t, userID, payload.UserID)
}

// The two queues are distinct, which is what keeps organization work from
// queueing behind outbound page fetches.
func TestProducer_EnrichmentAndOrganizationUseSeparateQueues(t *testing.T) {
	flushEnrichmentQueue(t)
	flushOrganizationQueue(t)

	require.NotEqual(
		t,
		queue.QueueEnrichment,
		queue.QueueOrganization,
		"enrichment and organization must not share a queue",
	)

	producer := queue.NewProducer(startRedisForTests(t))

	require.NoError(
		t,
		producer.EnqueueSavedItemEnrichment(
			context.Background(),
			uuid.New(),
		),
	)
	require.NoError(
		t,
		producer.EnqueueSavedItemOrganization(
			context.Background(),
			uuid.New(),
			uuid.New(),
		),
	)

	enrichmentTasks, err := inspectorForQueue(t).
		ListPendingTasks(queue.QueueEnrichment)
	require.NoError(t, err)
	require.Len(t, enrichmentTasks, 1)

	organizationTasks, err := inspectorForQueue(t).
		ListPendingTasks(queue.QueueOrganization)
	require.NoError(t, err)
	require.Len(t, organizationTasks, 1)

	require.Equal(t, queue.TaskTypeEnrichSavedItem, enrichmentTasks[0].Type)
	require.Equal(
		t,
		queue.TaskTypeOrganizeSavedItem,
		organizationTasks[0].Type,
	)
}

// pendingOrganizationTaskID waits for the organization queue to hold one pending
// task and returns its id.
func pendingOrganizationTaskID(t *testing.T) string {
	t.Helper()

	var id string

	require.Eventually(
		t,
		func() bool {
			tasks, err := inspectorForQueue(t).
				ListPendingTasks(queue.QueueOrganization)
			require.NoError(t, err)

			if len(tasks) != 1 {
				return false
			}

			id = tasks[0].ID

			return true
		},
		10*time.Second,
		25*time.Millisecond,
		"expected exactly one pending organization task",
	)

	return id
}

// A save through the real app with a real producer, asserted on the task that
// reaches the queue. The id in the payload is the row the save committed, which
// is the one thing the worker has to work from.
func TestSaveEnqueuesTheCommittedSavedItemID(t *testing.T) {
	truncateWorkerEnrichmentData(t)
	flushEnrichmentQueue(t)

	userID := createWorkerEnrichmentUser(t, "producer-committed@example.com")
	createUnsortedForWorkerEnrichment(t, userID)

	app, _ := testutil.NewAppWithEnqueuer(
		testPool,
		&testutil.FakeEnricher{},
		queue.NewProducer(startRedisForTests(t)),
	)

	_, body := saveURL(
		t,
		app,
		userID,
		"https://example.com/articles/producer",
	)

	savedItemID := mustParseUUID(t, body.Data.SavedItem.ID)

	info := awaitPendingEnrichmentTask(t)

	var payload queue.EnrichSavedItemPayload

	require.NoError(t, json.Unmarshal([]byte(info.Payload), &payload))

	require.Equal(
		t,
		savedItemID,
		payload.SavedItemID,
		"the queued task must name the row the save committed",
	)

	// The row really exists, so a worker will find a URL to fetch rather than a
	// task pointing at an id the database never accepted.
	state := workerEnrichmentState(t, savedItemID)

	require.Equal(t, savedItemID, state.ID)
	require.Equal(t, "pending", state.EnrichmentStatus)
}
