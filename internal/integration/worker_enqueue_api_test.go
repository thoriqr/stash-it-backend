package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/testutil"
	workerenrichmentdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/worker_enrichment/generated"
)

// These tests cover the enqueue boundary on the API side: what a save puts on the
// queue, and what happens when it cannot.
//
// The queue itself is a fake here. What belongs to this API is the decision that
// a task is due and the id it names; that the task reaches a worker is covered
// end to end in worker_queue_test.go.

type createSavedItemResponse struct {
	Data struct {
		SavedItem struct {
			ID       string  `json:"id"`
			URL      string  `json:"url"`
			Domain   *string `json:"domain"`
			Platform *string `json:"platform"`
			Title    *string `json:"title"`
		} `json:"saved_item"`
	} `json:"data"`
	Message string `json:"message"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// saveURL posts a URL and returns the decoded response.
func saveURL(
	t *testing.T,
	app *fiber.App,
	userID uuid.UUID,
	rawURL string,
) (*http.Response, createSavedItemResponse) {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/saved-items",
		strings.NewReader(`{"url": "`+rawURL+`"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := app.Test(req)
	require.NoError(t, err)

	var body createSavedItemResponse

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	return resp, body
}

// A save queues exactly one task, naming the committed row.
func TestSavedItem_Create_QueuesBackgroundEnrichment(t *testing.T) {
	ctx := context.Background()

	require.NoError(
		t,
		workerenrichmentdbtest.New(testPool).
			TruncateWorkerEnrichmentData(ctx),
	)

	userID := createWorkerEnrichmentUser(t, "enqueue@example.com")
	createUnsortedForWorkerEnrichment(t, userID)

	enqueuer := &testutil.FakeSavedItemEnqueuer{}

	app, _ := testutil.NewAppWithEnqueuer(
		testPool,
		&testutil.FakeEnricher{},
		enqueuer,
	)

	resp, body := saveURL(
		t,
		app,
		userID,
		"https://example.com/articles/queued",
	)

	require.Equal(t, http.StatusCreated, resp.StatusCode)

	savedItemID := mustParseUUID(t, body.Data.SavedItem.ID)

	require.Equal(
		t,
		[]uuid.UUID{savedItemID},
		enqueuer.Queued(),
		"the task must name the row the save just committed",
	)
}

// The save response carries no metadata and the row is still pending when the
// request returns. Nothing about the save depends on a fetch having happened.
func TestSavedItem_Create_DoesNotWaitForEnrichment(t *testing.T) {
	ctx := context.Background()

	require.NoError(
		t,
		workerenrichmentdbtest.New(testPool).
			TruncateWorkerEnrichmentData(ctx),
	)

	userID := createWorkerEnrichmentUser(t, "enqueue-pending@example.com")
	createUnsortedForWorkerEnrichment(t, userID)

	enqueuer := &testutil.FakeSavedItemEnqueuer{}

	// The enricher would report metadata if it were ever consulted by the save
	// path. It is not, so nothing it holds can reach the response.
	enricher := &testutil.FakeEnricher{}

	app, _ := testutil.NewAppWithEnqueuer(testPool, enricher, enqueuer)

	resp, body := saveURL(t, app, userID, "https://example.com/articles/quiet")

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.Nil(t, body.Data.SavedItem.Platform)
	require.Nil(t, body.Data.SavedItem.Title)

	state := workerEnrichmentState(
		t,
		mustParseUUID(t, body.Data.SavedItem.ID),
	)

	require.Equal(
		t,
		"pending",
		state.EnrichmentStatus,
		"a save must leave the item pending until enrichment actually runs",
	)
	require.False(t, state.LastEnrichedAt.Valid)
	require.Empty(
		t,
		enricher.RequestedURLs(),
		"saving must not fetch the remote source",
	)
}

// A queue that cannot be reached must not turn a committed save into a failed
// request. The row is valid product data whether or not its metadata was ever
// fetched, and enrichment can still be asked for on demand.
func TestSavedItem_Create_SucceedsWhenTheQueueIsUnreachable(t *testing.T) {
	ctx := context.Background()

	require.NoError(
		t,
		workerenrichmentdbtest.New(testPool).
			TruncateWorkerEnrichmentData(ctx),
	)

	userID := createWorkerEnrichmentUser(t, "enqueue-down@example.com")
	createUnsortedForWorkerEnrichment(t, userID)

	enqueuer := &testutil.FakeSavedItemEnqueuer{
		Err: errors.New("dial tcp 127.0.0.1:6379: connect: connection refused"),
	}

	app, _ := testutil.NewAppWithEnqueuer(
		testPool,
		&testutil.FakeEnricher{},
		enqueuer,
	)

	resp, body := saveURL(t, app, userID, "https://example.com/articles/orphan")

	require.Equal(t, http.StatusCreated, resp.StatusCode)

	savedItemID := mustParseUUID(t, body.Data.SavedItem.ID)

	// The save is reported as a success and the row is really there, which is the
	// only outcome that keeps "saving" independent of everything optional.
	state := workerEnrichmentState(t, savedItemID)

	require.Equal(t, userID, state.UserID)
	require.Equal(t, "https://example.com/articles/orphan", state.Url)
	require.Equal(t, "pending", state.EnrichmentStatus)
}

// Reading or deleting an item must never queue anything. Only saving starts
// enrichment, and a task per delete would enrich items on their way out.
func TestSavedItem_NonCreatingReadsDoNotQueue(t *testing.T) {
	ctx := context.Background()

	require.NoError(
		t,
		workerenrichmentdbtest.New(testPool).
			TruncateWorkerEnrichmentData(ctx),
	)

	userID := createWorkerEnrichmentUser(t, "enqueue-reads@example.com")
	unsortedID := createUnsortedForWorkerEnrichment(t, userID)

	enqueuer := &testutil.FakeSavedItemEnqueuer{}

	app, _ := testutil.NewAppWithEnqueuer(
		testPool,
		&testutil.FakeEnricher{},
		enqueuer,
	)

	_, body := saveURL(t, app, userID, "https://example.com/articles/read")
	savedItemID := mustParseUUID(t, body.Data.SavedItem.ID)

	require.Len(t, enqueuer.Queued(), 1)

	token := newTestAccessToken(t, userID)

	getReq := httptest.NewRequest(
		http.MethodGet,
		"/saved-items/"+savedItemID.String(),
		nil,
	)
	getReq.Header.Set("Authorization", "Bearer "+token)

	getResp, err := app.Test(getReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	// Listing is a read too, so browsing a collection must not queue anything. The
	// listing lives on the collection because that is where every saved item belongs.
	listReq := httptest.NewRequest(
		http.MethodGet,
		"/collections/"+unsortedID.String()+"/saved-items",
		nil,
	)
	listReq.Header.Set("Authorization", "Bearer "+token)

	listResp, err := app.Test(listReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listResp.StatusCode)

	deleteReq := httptest.NewRequest(
		http.MethodDelete,
		"/saved-items/"+savedItemID.String(),
		nil,
	)
	deleteReq.Header.Set("Authorization", "Bearer "+token)

	deleteResp, err := app.Test(deleteReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, deleteResp.StatusCode)

	require.Len(
		t,
		enqueuer.Queued(),
		1,
		"only creating a saved item may queue enrichment",
	)
}
