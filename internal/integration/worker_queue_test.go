package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/logger"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	workerenrichmentdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/worker_enrichment/generated"
	workerenrichment "github.com/thoriqr/stash-it-backend/internal/worker/enrichment"
	enrichmentdb "github.com/thoriqr/stash-it-backend/internal/worker/enrichment/generated"
	"github.com/thoriqr/stash-it-backend/internal/worker/queue"
)

// failOnceEnricher fails with a classified error and then succeeds, standing in
// for an origin that was briefly unavailable.
//
// It exists because retrying is a property of two things at once: the handler
// deciding not to give up, and Asynq actually performing another attempt. A fake
// that always fails cannot show the second, and a fake that always succeeds
// cannot show the first. The permanent flag makes it fail every time instead, for
// the test that needs to watch the retry budget run out.
type failOnceEnricher struct {
	// failure is returned instead of Metadata.
	failure error

	// metadata is returned by every attempt that succeeds.
	metadata enrichmentcore.Metadata

	// permanent makes every attempt fail rather than only the first.
	permanent bool

	mutex sync.Mutex
	calls int
}

func (f *failOnceEnricher) Enrich(
	_ context.Context,
	_ string,
) (enrichmentcore.Metadata, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	f.calls++

	if f.permanent || f.calls == 1 {
		return enrichmentcore.Metadata{}, f.failure
	}

	return f.metadata, nil
}

// Calls reports how many times the enricher was asked to enrich.
func (f *failOnceEnricher) Calls() int {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	return f.calls
}

// validWorkerTestText returns a pointer to value, for building the metadata a
// successful attempt returns.
func validWorkerTestText(value string) *string {
	return &value
}

// These tests drive the whole background path across real infrastructure: a real
// POST /saved-items that enqueues into a real Redis, a real Asynq worker
// consuming it, and the worker's own sqlc target writing the result to real
// Postgres.
//
// Everything between those two ends is the real thing, including the retry delay
// function and the queue name, because the whole point is to prove the producer
// and the consumer agree on them. Only the extraction layer is faked, for the
// reason the other enrichment tests fake it: the guarded outbound client refuses
// loopback, and what matters here is the wiring and the row the worker writes, not
// what an HTML parser finds.
//
// They are the only tests in this package that need Redis, so it is started on
// first use rather than made a precondition for the whole suite.

// startTestWorker runs a real Asynq server against the test Redis with the
// worker's own handler registered, and stops it when the test ends.
//
// It is built from the same package-level pieces the worker binary uses, so a
// change to the task type, the queue name or the backoff has to be reflected here
// or the test stops finding anything.
func startTestWorker(
	t *testing.T,
	enricher workerenrichment.MetadataEnricher,
) {
	t.Helper()

	startTestWorkerWithRetryDelay(
		t,
		enricher,
		queue.EnrichmentRetryDelay,
	)
}

// startTestWorkerWithRetryDelay is startTestWorker with the retry delay chosen by
// the caller.
//
// It exists so a test that needs to observe a retry does not have to wait out the
// production backoff, whose first delay is deliberately tens of seconds. The
// production delay function itself is asserted separately, in
// internal/worker/queue, so shortening it here does not weaken what production
// does; it only stops a test from taking a minute to reach its second attempt.
func startTestWorkerWithRetryDelay(
	t *testing.T,
	enricher workerenrichment.MetadataEnricher,
	retryDelay asynq.RetryDelayFunc,
) {
	t.Helper()

	// No organizer: this file asserts what the enrichment worker itself writes, and
	// the handoff to organization is covered in worker_organization_test.go, which
	// starts a worker with a real producer.
	startTestWorkerWithRetryDelayAndOrganizer(
		t,
		enricher,
		retryDelay,
		nil,
	)
}

// startTestWorkerWithRetryDelayAndOrganizer is startTestWorkerWithRetryDelay with
// the organizer the enrichment service hands successful enrichments to.
//
// Passing nil is how a test says the enrichment worker schedules nothing; passing
// the real producer is how a test observes the handoff end to end.
func startTestWorkerWithRetryDelayAndOrganizer(
	t *testing.T,
	enricher workerenrichment.MetadataEnricher,
	retryDelay asynq.RetryDelayFunc,
	organizer workerenrichment.SavedItemOrganizer,
) {
	t.Helper()

	redisClient := startRedisForTests(t)

	log, err := logger.New("development")
	require.NoError(t, err)

	service := workerenrichment.NewService(
		workerenrichment.NewRepository(enrichmentdb.New(testPool)),
		enricher,
		organizer,
		log,
	)

	server := asynq.NewServerFromRedisClient(
		redisClient,
		asynq.Config{
			Concurrency:     1,
			Queues:          map[string]int{queue.QueueEnrichment: 1},
			RetryDelayFunc:  retryDelay,
			Logger:          logger.Asynq(log),
			ShutdownTimeout: 5 * time.Second,
		},
	)

	mux := asynq.NewServeMux()
	mux.Handle(
		queue.TaskTypeEnrichSavedItem,
		workerenrichment.NewHandler(service, log),
	)

	require.NoError(t, server.Start(mux))

	t.Cleanup(server.Shutdown)
}

// testRetryDelay is a retry delay for tests that need to watch a retry happen.
// It is installed on the test server only. Production keeps
// queue.EnrichmentRetryDelay and its tens-of-seconds base, and that function is
// asserted on its own in internal/worker/queue.
func testRetryDelay(int, error, *asynq.Task) time.Duration {
	return 50 * time.Millisecond
}

// queueState is the part of Asynq's own accounting these tests assert on.
//
// It is read through asynq's Inspector rather than by counting Redis keys by
// hand. Asynq's key layout is prefixed and hash-tagged, and hand-built key names
// would silently report zero for every task and make these assertions pass without
// ever having looked at anything.
type queueState struct {
	pending  int
	active   int
	retry    int
	archived int
}

func startRedisForTests(t *testing.T) redis.UniversalClient {
	t.Helper()

	client, err := testutil.StartRedisForTests(context.Background())
	require.NoError(t, err)

	return client
}

// unreachableRedisForTests returns a client pointed at a port nothing listens
// on, standing in for a queue that cannot be reached.
//
// The shared test Redis is deliberately not used for this: the point is a
// connection that fails, not one that succeeds.
func unreachableRedisForTests(t *testing.T) redis.UniversalClient {
	t.Helper()

	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1",
		DB:   3,
		// Fail immediately rather than retrying, so the test is not waiting on a
		// backoff to discover what is already already known.
		MaxRetries:   -1,
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})

	t.Cleanup(func() {
		_ = client.Close()
	})

	return client
}

// inspectorForQueue opens an Inspector on the test Redis.
//
// The Inspector keeps its own connection and is closed with the test.
func inspectorForQueue(t *testing.T) *asynq.Inspector {
	t.Helper()

	inspector := asynq.NewInspectorFromRedisClient(startRedisForTests(t))

	t.Cleanup(func() {
		_ = inspector.Close()
	})

	return inspector
}

// readQueueState reports how many tasks are waiting, running, scheduled for retry,
// and archived.
//
// The queue's accounting is read rather than the database's, because the claim
// under test is about the queue: a task that was discarded must not be sitting in
// the retry set waiting to be picked up again, and a permanent failure must be
// archived on its first attempt rather than only once the retry budget runs out.
func readQueueState(t *testing.T) queueState {
	t.Helper()

	info, err := inspectorForQueue(t).GetQueueInfo(queue.QueueEnrichment)
	if err != nil {
		// A queue that does not exist yet holds no tasks, which is the state before
		// anything has been enqueued.
		return queueState{}
	}

	return queueState{
		pending:  info.Pending,
		active:   info.Active,
		retry:    info.Retry,
		archived: info.Archived,
	}
}

// queueIsEmpty reports that nothing is left waiting, running or rescheduled.
func queueIsEmpty(state queueState) bool {
	return state.pending == 0 &&
		state.active == 0 &&
		state.retry == 0 &&
		state.archived == 0
}

// flushEnrichmentQueue empties the queue before a test.
//
// The container and the namespace are shared by this file on purpose, because
// separate connections would not give these tests separate queues anyway. What has
// to be avoided is one test's task being consumed while another test is asserting
// on its own item.
func flushEnrichmentQueue(t *testing.T) {
	t.Helper()

	inspector := inspectorForQueue(t)

	// Asynq reports a queue that has never held a task as missing, and it wraps
	// that in an internal error type. Checking for the queue's existence through
	// the public API is used instead of matching on that error, so this helper does
	// not depend on how Asynq chooses to wrap it.
	queues, err := inspector.Queues()
	require.NoError(t, err)

	if !slices.Contains(queues, queue.QueueEnrichment) {
		return
	}

	for _, deleteTasks := range []func(string) (int, error){
		inspector.DeleteAllPendingTasks,
		inspector.DeleteAllRetryTasks,
		inspector.DeleteAllScheduledTasks,
		inspector.DeleteAllArchivedTasks,
		inspector.DeleteAllCompletedTasks,
	} {
		_, err := deleteTasks(queue.QueueEnrichment)
		require.NoError(t, err)
	}
}

// awaitEnrichmentStatus waits for the worker to record the expected status,
// because the entire point of the design is that the save returns long before any
// of this happens.
func awaitEnrichmentStatus(
	t *testing.T,
	savedItemID uuid.UUID,
	expected string,
) workerenrichmentdbtest.GetSavedItemWorkerEnrichmentStateRow {
	t.Helper()

	var last workerenrichmentdbtest.GetSavedItemWorkerEnrichmentStateRow

	require.Eventually(
		t,
		func() bool {
			state, err := workerenrichmentdbtest.New(testPool).
				GetSavedItemWorkerEnrichmentState(
					context.Background(),
					savedItemID,
				)

			if err != nil {
				return false
			}

			last = state

			return state.EnrichmentStatus == expected
		},
		30*time.Second,
		50*time.Millisecond,
		"the worker never recorded enrichment status %q; last saw %q",
		expected,
		last.EnrichmentStatus,
	)

	return last
}

// createSavedItemThroughAPI saves a URL through the real app with a real queue
// producer wired in, and returns the new item's id.
func createSavedItemThroughAPI(
	t *testing.T,
	userID uuid.UUID,
	rawURL string,
) uuid.UUID {
	t.Helper()

	redisClient := startRedisForTests(t)

	// The real producer, not the fake: this test exists to prove the task actually
	// reaches the queue the worker reads from.
	app, _ := testutil.NewAppWithEnqueuer(
		testPool,
		&testutil.FakeEnricher{},
		queue.NewProducer(redisClient),
	)

	req := httptest.NewRequest(
		http.MethodPost,
		"/saved-items",
		strings.NewReader(`{"url": "`+rawURL+`"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var body struct {
		Data struct {
			SavedItem struct {
				ID       string  `json:"id"`
				Platform *string `json:"platform"`
				Title    *string `json:"title"`
			} `json:"saved_item"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotEmpty(t, body.Data.SavedItem.ID)

	// The save response carries no metadata, which is the observable half of
	// "saving never waits for enrichment".
	require.Nil(t, body.Data.SavedItem.Platform)
	require.Nil(t, body.Data.SavedItem.Title)

	return mustParseUUID(t, body.Data.SavedItem.ID)
}

// The whole path: a save queues work, the worker consumes it, and the row ends up
// enriched.
func TestBackgroundEnrichment_SaveQueuesAndWorkerEnriches(t *testing.T) {
	truncateWorkerEnrichmentData(t)
	flushEnrichmentQueue(t)

	title := "A queued article"
	platform := "example"
	description := "From a real queue"
	imageURL := "https://example.com/og.png"

	enricher := &testutil.FakeEnricher{
		Metadata: enrichmentcore.Metadata{
			Title:       &title,
			Platform:    &platform,
			Description: &description,
			ImageURL:    &imageURL,
		},
	}

	startTestWorker(t, enricher)

	userID := createWorkerEnrichmentUser(t, "queue-e2e@example.com")
	createUnsortedForWorkerEnrichment(t, userID)

	savedItemID := createSavedItemThroughAPI(
		t,
		userID,
		"https://example.com/articles/queued",
	)

	state := awaitEnrichmentStatus(t, savedItemID, "completed")

	require.Equal(t, title, state.Title.String)
	require.Equal(t, platform, state.Platform.String)
	require.Equal(t, description, state.Description.String)
	require.Equal(t, imageURL, state.ImageUrl.String)
	require.True(t, state.LastEnrichedAt.Valid)

	// The worker fetched the URL stored on the row, which is the reason the
	// payload carries only an id.
	require.Equal(
		t,
		[]string{"https://example.com/articles/queued"},
		enricher.RequestedURLs(),
	)

	require.True(
		t,
		queueIsEmpty(readQueueState(t)),
		"the queue must be empty once the task has been consumed",
	)
}

// A task whose item was deleted finishes rather than being retried. There is
// nothing on the far side of a retry that could bring the item back.
func TestBackgroundEnrichment_DeletedItemIsDiscardedNotRetried(t *testing.T) {
	truncateWorkerEnrichmentData(t)
	flushEnrichmentQueue(t)

	enricher := &testutil.FakeEnricher{}

	startTestWorker(t, enricher)

	userID := createWorkerEnrichmentUser(t, "queue-deleted@example.com")
	collectionID := createUnsortedForWorkerEnrichment(t, userID)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/deleted",
	)

	redisClient := startRedisForTests(t)
	producer := queue.NewProducer(redisClient)

	// Queued first, then deleted, so the worker finds a row that is not there.
	require.NoError(
		t,
		producer.EnqueueSavedItemEnrichment(
			context.Background(),
			savedItemID,
		),
	)

	require.NoError(
		t,
		workerenrichmentdbtest.New(testPool).
			DeleteSavedItemForWorkerEnrichment(
				context.Background(),
				savedItemID,
			),
	)

	// The queue empties without the task reappearing in the retry set, and without
	// being archived either: the handler returns nil, which is how Asynq is told a
	// task is done. A discarded task leaves nothing behind to be inspected later.
	require.Eventually(
		t,
		func() bool {
			return queueIsEmpty(readQueueState(t))
		},
		30*time.Second,
		50*time.Millisecond,
		"a task for a deleted item must finish rather than be retried",
	)

	require.Empty(
		t,
		enricher.RequestedURLs(),
		"no outbound request may be made for a deleted item",
	)
}

// A permanently failing page is recorded as failed and its task is archived
// rather than retried. This is the end state the retry classification produces, and
// it is asserted on the row rather than on a delay nobody would wait for.
func TestBackgroundEnrichment_PermanentFailureIsNotRetried(t *testing.T) {
	truncateWorkerEnrichmentData(t)
	flushEnrichmentQueue(t)

	enricher := &testutil.FakeEnricher{
		Err: &enrichmentcore.Failure{
			Kind: enrichmentcore.FailureContent,
			Err:  context.DeadlineExceeded,
		},
	}

	startTestWorker(t, enricher)

	userID := createWorkerEnrichmentUser(t, "queue-permanent@example.com")
	collectionID := createUnsortedForWorkerEnrichment(t, userID)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/not-html",
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

	// Archived means "stopped retrying", not "retried and given up". Only the second
	// would mean the worker had spent the whole retry budget on a page that is not
	// going to change: the task is archived on its first attempt.
	require.Eventually(
		t,
		func() bool {
			state := readQueueState(t)

			return state.retry == 0 && state.pending == 0 && state.archived == 1
		},
		30*time.Second,
		50*time.Millisecond,
		"a permanent failure must be archived rather than retried",
	)

	// One attempt only. A page that answered with something that is not an HTML
	// document will answer the same way next time, and the retry budget exists for
	// the failures that would not.
	require.Equal(
		t,
		1,
		len(enricher.RequestedURLs()),
		"a permanent failure must not be fetched again",
	)
}

// A transient failure is retried rather than given up on, and a retry that then
// succeeds leaves the item completed. This is the behaviour the bounded retry
// budget exists for, and the one the permanent-failure test above is the mirror
// image of.
//
// It asserts on what Asynq actually did rather than on the handler's return
// value: the task must be seen in the retry set, and it must be picked up again
// from there. The status write is asserted at the end, so a task that "succeeded"
// without ever being retried would not pass.
func TestBackgroundEnrichment_TransientFailureIsRetriedThenCompletes(t *testing.T) {
	truncateWorkerEnrichmentData(t)
	flushEnrichmentQueue(t)

	// Fails with a fetch classification once, which is what a DNS failure, a
	// dropped connection or a 503 looks like, then succeeds.
	enricher := &failOnceEnricher{
		failure: &enrichmentcore.Failure{
			Kind: enrichmentcore.FailureFetch,
			Err:  errors.New("dial tcp: i/o timeout"),
		},
		metadata: enrichmentcore.Metadata{
			Title:    validWorkerTestText("Recovered article"),
			Platform: validWorkerTestText("example"),
		},
	}

	startTestWorkerWithRetryDelay(t, enricher, testRetryDelay)

	userID := createWorkerEnrichmentUser(t, "queue-retry@example.com")
	collectionID := createUnsortedForWorkerEnrichment(t, userID)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/flaky-origin",
	)

	producer := queue.NewProducer(startRedisForTests(t))

	require.NoError(
		t,
		producer.EnqueueSavedItemEnrichment(
			context.Background(),
			savedItemID,
		),
	)

	// The first attempt must be observable in the retry set. Nothing else
	// distinguishes a real Asynq retry from the handler merely deciding to allow
	// one, so this is the assertion that makes the rest mean anything.
	require.Eventually(
		t,
		func() bool {
			return readQueueState(t).retry == 1
		},
		30*time.Second,
		10*time.Millisecond,
		"a retryable failure must leave the task in the retry set",
	)

	state := awaitEnrichmentStatus(t, savedItemID, "completed")

	// Two attempts: the first failed and was retried, the second succeeded.
	require.Equal(
		t,
		2,
		enricher.Calls(),
		"the task must be attempted again after a retryable failure",
	)
	require.Equal(
		t,
		"Recovered article",
		state.Title.String,
		"a successful retry must write what the origin reports",
	)
	require.Equal(t, "example", state.Platform.String)
	require.True(
		t,
		state.LastEnrichedAt.Valid,
		"the successful attempt is when metadata was refreshed",
	)

	require.True(
		t,
		queueIsEmpty(readQueueState(t)),
		"a task that eventually succeeded must leave nothing behind",
	)
}

// A retryable failure is recorded as failed, and the retry that follows does not
// require any intermediate state on the row: the schema has exactly three values
// and none of them means "being retried".
func TestBackgroundEnrichment_RetryPersistsNoIntermediateStatus(t *testing.T) {
	truncateWorkerEnrichmentData(t)
	flushEnrichmentQueue(t)

	// Always fails as a fetch failure, so the item is left failed while the task
	// is still inside its retry budget.
	enricher := &failOnceEnricher{
		permanent: true,
		failure: &enrichmentcore.Failure{
			Kind: enrichmentcore.FailureFetch,
			Err:  errors.New("dial tcp: i/o timeout"),
		},
	}

	startTestWorkerWithRetryDelay(t, enricher, testRetryDelay)

	userID := createWorkerEnrichmentUser(t, "queue-retry-failed@example.com")
	collectionID := createUnsortedForWorkerEnrichment(t, userID)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/still-down",
	)

	producer := queue.NewProducer(startRedisForTests(t))

	require.NoError(
		t,
		producer.EnqueueSavedItemEnrichment(
			context.Background(),
			savedItemID,
		),
	)

	state := awaitEnrichmentStatus(t, savedItemID, "failed")

	// The one status a failed attempt may leave, whether or not more attempts
	// follow.
	require.Equal(t, "failed", state.EnrichmentStatus)

	// A failed attempt refreshes nothing: no metadata was extracted, so there is
	// nothing to store, and last_enriched_at is not this moment.
	require.False(
		t,
		state.Title.Valid,
		"a failed attempt must not write metadata",
	)
	require.False(t, state.Platform.Valid)
	require.False(t, state.Description.Valid)
	require.False(t, state.ImageUrl.Valid)
	require.False(
		t,
		state.LastEnrichedAt.Valid,
		"a failed attempt is not when metadata was refreshed",
	)

	// The item is untouched in every other respect. A failed enrichment is still a
	// valid saved item.
	require.Equal(t, collectionID, state.CollectionID)
	require.Equal(
		t,
		"https://example.com/still-down",
		state.Url,
	)
	require.Equal(t, userID, state.UserID)
}

// Once the retry budget is exhausted the worker finishes: the task is archived
// rather than left to be retried forever, and the item carries the failure. This
// is what bounds a retryable failure the same way a permanent one is bounded.
func TestBackgroundEnrichment_RetryBudgetIsBoundedAndFinishes(t *testing.T) {
	truncateWorkerEnrichmentData(t)
	flushEnrichmentQueue(t)

	enricher := &failOnceEnricher{
		permanent: true,
		failure: &enrichmentcore.Failure{
			Kind: enrichmentcore.FailureFetch,
			Err:  errors.New("dial tcp: i/o timeout"),
		},
	}

	startTestWorkerWithRetryDelay(t, enricher, testRetryDelay)

	userID := createWorkerEnrichmentUser(t, "queue-retry-budget@example.com")
	collectionID := createUnsortedForWorkerEnrichment(t, userID)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/permanently-down",
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

	// Enqueue with the configured budget: one attempt plus EnrichmentMaxRetry
	// retries, and then the task is archived. Nothing is left waiting or
	// rescheduled.
	require.Eventually(
		t,
		func() bool {
			state := readQueueState(t)

			return state.archived == 1 &&
				state.pending == 0 &&
				state.active == 0
		},
		60*time.Second,
		20*time.Millisecond,
		"an exhausted retry budget must archive the task rather than retry forever",
	)

	require.Equal(
		t,
		queue.EnrichmentMaxRetry+1,
		enricher.Calls(),
		"the task must be attempted once and then exactly as often as the budget allows",
	)

	require.Equal(
		t,
		"failed",
		workerEnrichmentState(t, savedItemID).EnrichmentStatus,
		"an exhausted budget leaves the item failed",
	)
}

// The background path and the synchronous endpoint are two callers of the same
// statements, so they must leave identical rows for the same page.
func TestBackgroundEnrichment_MatchesTheSynchronousResult(t *testing.T) {
	truncateWorkerEnrichmentData(t)

	userID := createWorkerEnrichmentUser(t, "queue-parity@example.com")
	collectionID := createUnsortedForWorkerEnrichment(t, userID)

	title := "Parity"

	metadata := enrichmentcore.Metadata{Title: &title}

	backgroundID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/parity",
	)

	require.NoError(
		t,
		newRealWorkerService(t, &testutil.FakeEnricher{Metadata: metadata}).
			EnrichSavedItem(context.Background(), backgroundID),
	)

	synchronousID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/parity",
	)

	app := newEnrichmentApp(&testutil.FakeEnricher{Metadata: metadata})

	resp, body := enrichSavedItem(t, app, userID, synchronousID)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "completed", body.Data.SavedItem.EnrichmentStatus)

	background := workerEnrichmentState(t, backgroundID)
	synchronous := workerEnrichmentState(t, synchronousID)

	require.Equal(t, background.EnrichmentStatus, synchronous.EnrichmentStatus)
	require.Equal(t, background.Title.String, synchronous.Title.String)
	require.Equal(t, background.Platform.String, synchronous.Platform.String)
	require.Equal(t, background.Description.String, synchronous.Description.String)
	require.Equal(t, background.ImageUrl.String, synchronous.ImageUrl.String)
	require.Equal(t, background.CollectionID, synchronous.CollectionID)
}
