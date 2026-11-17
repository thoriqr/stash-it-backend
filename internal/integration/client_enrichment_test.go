package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/hostset"
	"github.com/thoriqr/stash-it-backend/internal/logger"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	workerenrichment "github.com/thoriqr/stash-it-backend/internal/worker/enrichment"
	enrichmentdb "github.com/thoriqr/stash-it-backend/internal/worker/enrichment/generated"
)

// These tests drive the client-assisted path end to end: a save of an
// allowlisted host is not queued, and the enrich endpoint stores what the client
// extracted instead of fetching.

const instagramReelURL = "https://www.instagram.com/reel/DfzezRYhSSn/"

var clientEnrichmentHosts = []string{"www.instagram.com", "instagram.com"}

func newClientEnrichmentApp(
	t *testing.T,
	enricher *testutil.FakeEnricher,
	enqueuer *testutil.FakeSavedItemEnqueuer,
) *fiber.App {
	t.Helper()

	app, _ := testutil.NewAppWithClientEnrichmentHosts(
		testPool,
		enricher,
		enqueuer,
		clientEnrichmentHosts,
	)

	return app
}

// A save of an allowlisted host without metadata is stored as failed and is
// never queued.
func TestClientEnrichment_SaveWithoutMetadataIsFailedAndNotQueued(t *testing.T) {
	truncateEnrichmentData(t)

	userID := createEnrichmentUser(t, "client-save@example.com")
	createUnsortedForEnrichment(t, userID)

	enricher := &testutil.FakeEnricher{}
	enqueuer := &testutil.FakeSavedItemEnqueuer{}

	app := newClientEnrichmentApp(t, enricher, enqueuer)

	resp, body := saveURL(t, app, userID, instagramReelURL)

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.Equal(t, "failed", body.Data.SavedItem.EnrichmentStatus)

	// The two assertions that make this the client-assisted path.
	require.Empty(t, enqueuer.Queued(), "no enrichment task for a client-owned host")
	require.Empty(t, enricher.RequestedURLs(), "the save must not fetch")
}

// Metadata on the save is stored with the row, completed, and still not queued.
func TestClientEnrichment_SaveStoresMetadataAndIsNotQueued(t *testing.T) {
	truncateEnrichmentData(t)

	userID := createEnrichmentUser(t, "client-save-metadata@example.com")
	createUnsortedForEnrichment(t, userID)

	enricher := &testutil.FakeEnricher{}
	enqueuer := &testutil.FakeSavedItemEnqueuer{}

	app := newClientEnrichmentApp(t, enricher, enqueuer)

	req := httptest.NewRequest(
		http.MethodPost,
		"/saved-items",
		strings.NewReader(`{"url":"`+instagramReelURL+`","note":"keep this",`+
			`"metadata":{"title":"Shark Detail on Instagram: \"a caption\"",`+
			`"description":"315K likes - sharkdetail_ on November 16, 2026: \"a caption\"",`+
			`"image_url":"https://cdn.example/preview.jpg","platform":"Instagram"}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var body createSavedItemResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(t, "completed", body.Data.SavedItem.EnrichmentStatus)

	require.Empty(t, enqueuer.Queued())
	require.Empty(t, enricher.RequestedURLs())

	// The detail read proves the metadata actually reached the row.
	itemID, parseErr := uuid.Parse(body.Data.SavedItem.ID)
	require.NoError(t, parseErr)

	state := getEnrichmentState(t, itemID)
	require.Equal(t, "completed", state.EnrichmentStatus)
	require.Equal(t, "Shark Detail on Instagram", state.Title.String)
	require.Equal(t, "315K likes - sharkdetail_ on November 16, 2026: \"a caption\"", state.Description.String)
	require.Equal(t, "https://cdn.example/preview.jpg", state.ImageUrl.String)
	require.Equal(t, "Instagram", state.Platform.String)
	require.True(t, state.LastEnrichedAt.Valid)
}

// Client metadata is stored, the item is completed, and nothing is fetched.
func TestClientEnrichment_EnrichStoresClientMetadata(t *testing.T) {
	truncateEnrichmentData(t)

	userID := createEnrichmentUser(t, "client-enrich@example.com")
	collectionID := createUnsortedForEnrichment(t, userID)
	savedItemID := createPendingSavedItem(t, userID, collectionID, instagramReelURL)

	enricher := &testutil.FakeEnricher{}
	enqueuer := &testutil.FakeSavedItemEnqueuer{}

	app := newClientEnrichmentApp(t, enricher, enqueuer)

	req := httptest.NewRequest(
		http.MethodPost,
		"/saved-items/"+savedItemID.String()+"/enrich",
		strings.NewReader(`{"metadata":{"title":"A reel","description":"A description",`+
			`"image_url":"https://cdn.example/preview.jpg","platform":"Instagram"}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, body := doEnrichRequest(t, app, req)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "completed", body.Data.SavedItem.EnrichmentStatus)
	require.Equal(t, "A reel", *body.Data.SavedItem.Title)
	require.Equal(t, "A description", *body.Data.SavedItem.Description)
	require.Equal(t, "https://cdn.example/preview.jpg", *body.Data.SavedItem.ImageURL)
	require.Equal(t, "Instagram", *body.Data.SavedItem.Platform)
	require.NotNil(t, body.Data.SavedItem.LastEnrichedAt)

	require.Empty(t, enricher.RequestedURLs(), "the enrich endpoint must not fetch this host")

	state := getEnrichmentState(t, savedItemID)
	require.Equal(t, "completed", state.EnrichmentStatus)
}

// No metadata means the client could not extract, which is recorded as failed.
func TestClientEnrichment_EnrichWithoutMetadataFails(t *testing.T) {
	truncateEnrichmentData(t)

	userID := createEnrichmentUser(t, "client-empty@example.com")
	collectionID := createUnsortedForEnrichment(t, userID)
	savedItemID := createPendingSavedItem(t, userID, collectionID, instagramReelURL)

	enricher := &testutil.FakeEnricher{}
	app := newClientEnrichmentApp(t, enricher, &testutil.FakeSavedItemEnqueuer{})

	req := httptest.NewRequest(
		http.MethodPost,
		"/saved-items/"+savedItemID.String()+"/enrich",
		strings.NewReader(`{"metadata":{}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, body := doEnrichRequest(t, app, req)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "failed", body.Data.SavedItem.EnrichmentStatus)
	require.Nil(t, body.Data.SavedItem.Title)
	require.Nil(t, body.Data.SavedItem.LastEnrichedAt)
	require.Empty(t, enricher.RequestedURLs())
}

// A host that is not allowlisted ignores the body and uses the server path,
// which is what keeps the change backward compatible.
func TestClientEnrichment_NonAllowlistedHostIgnoresMetadata(t *testing.T) {
	truncateEnrichmentData(t)

	userID := createEnrichmentUser(t, "server-host@example.com")
	collectionID := createUnsortedForEnrichment(t, userID)
	savedItemID := createPendingSavedItem(
		t,
		userID,
		collectionID,
		"https://example.com/article",
	)

	title := "From the server"
	enricher := &testutil.FakeEnricher{
		Metadata: enrichmentcore.Metadata{Title: &title},
	}

	app := newClientEnrichmentApp(t, enricher, &testutil.FakeSavedItemEnqueuer{})

	req := httptest.NewRequest(
		http.MethodPost,
		"/saved-items/"+savedItemID.String()+"/enrich",
		strings.NewReader(`{"metadata":{"title":"From the client"}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, body := doEnrichRequest(t, app, req)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "completed", body.Data.SavedItem.EnrichmentStatus)
	require.Equal(t, "From the server", *body.Data.SavedItem.Title)
	require.Equal(t, []string{"https://example.com/article"}, enricher.RequestedURLs())
}

// The item of one user is not enriched by another, allowlist or not.
func TestClientEnrichment_ForeignItemIsNotFound(t *testing.T) {
	truncateEnrichmentData(t)

	ownerID := createEnrichmentUser(t, "client-owner@example.com")
	collectionID := createUnsortedForEnrichment(t, ownerID)
	savedItemID := createPendingSavedItem(t, ownerID, collectionID, instagramReelURL)

	otherID := createEnrichmentUser(t, "client-other@example.com")

	app := newClientEnrichmentApp(
		t,
		&testutil.FakeEnricher{},
		&testutil.FakeSavedItemEnqueuer{},
	)

	req := httptest.NewRequest(
		http.MethodPost,
		"/saved-items/"+savedItemID.String()+"/enrich",
		strings.NewReader(`{"metadata":{"title":"Stolen"}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, otherID))

	resp, body := doEnrichRequest(t, app, req)

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "SAVED_ITEM_NOT_FOUND", body.Error.Code)

	state := getEnrichmentState(t, savedItemID)
	require.Equal(t, "pending", state.EnrichmentStatus)
}

// The worker gate is what protects an item that was queued before the allowlist
// existed. It records the failure without fetching and does not retry.
func TestClientEnrichment_WorkerSkipsAllowlistedHost(t *testing.T) {
	truncateWorkerEnrichmentData(t)

	userID := createWorkerEnrichmentUser(t, "client-worker@example.com")
	collectionID := createUnsortedForWorkerEnrichment(t, userID)
	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		instagramReelURL,
	)

	enricher := &testutil.FakeEnricher{}

	log, err := logger.New("development")
	require.NoError(t, err)

	service := workerenrichment.NewService(
		workerenrichment.NewRepository(enrichmentdb.New(testPool)),
		enricher,
		nil,
		hostset.New(clientEnrichmentHosts),
		log,
	)

	require.NoError(t, service.EnrichSavedItem(context.Background(), savedItemID))
	require.Empty(t, enricher.RequestedURLs(), "the worker must not fetch an allowlisted host")

	state := workerEnrichmentState(t, savedItemID)
	require.Equal(t, "failed", state.EnrichmentStatus)
}
