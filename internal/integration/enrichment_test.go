package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	enrichmentdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/enrichment/generated"
)

// These tests drive the enrichment endpoint end to end: the real app, the real
// auth middleware, handler, service, repository and database.
//
// Only the extraction layer is replaced. The real one is built around the guarded
// outbound HTTP client, and that client correctly refuses loopback, so a test
// could not reach an httptest server through it. Substituting a fake at the
// MetadataEnricher boundary also keeps the assertion on what the application
// does and persists, which is what belongs here; what the extractor finds is
// covered by the enrichment package's own tests.

func truncateEnrichmentData(t *testing.T) {
	t.Helper()

	db := enrichmentdbtest.New(testPool)

	require.NoError(t, db.TruncateEnrichmentData(context.Background()))
}

func createEnrichmentUser(t *testing.T, email string) uuid.UUID {
	t.Helper()

	db := enrichmentdbtest.New(testPool)

	userID, err := db.CreateEnrichmentUser(
		context.Background(),
		enrichmentdbtest.CreateEnrichmentUserParams{
			Email:       email,
			DisplayName: "Enrichment Test User",
		},
	)
	require.NoError(t, err)

	return userID
}

func createUnsortedForEnrichment(
	t *testing.T,
	userID uuid.UUID,
) uuid.UUID {
	t.Helper()

	db := enrichmentdbtest.New(testPool)

	collectionID, err := db.CreateUnsortedCollectionForEnrichment(
		context.Background(),
		userID,
	)
	require.NoError(t, err)

	return collectionID
}

func createUserCollectionForEnrichment(
	t *testing.T,
	userID uuid.UUID,
	name string,
) uuid.UUID {
	t.Helper()

	db := enrichmentdbtest.New(testPool)

	collectionID, err := db.CreateTestUserCollectionForEnrichment(
		context.Background(),
		enrichmentdbtest.CreateTestUserCollectionForEnrichmentParams{
			UserID: userID,
			Name:   name,
		},
	)
	require.NoError(t, err)

	return collectionID
}

// createPendingSavedItem creates a saved item the way a real save does: the
// enrichment columns are left to their defaults, which is pending with no
// last_enriched_at.
func createPendingSavedItem(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
	rawURL string,
) uuid.UUID {
	t.Helper()

	db := enrichmentdbtest.New(testPool)

	savedItemID, err := db.CreatePendingSavedItemForEnrichment(
		context.Background(),
		enrichmentdbtest.CreatePendingSavedItemForEnrichmentParams{
			UserID:       userID,
			Url:          rawURL,
			Domain:       testText("example.com"),
			CollectionID: collectionID,
		},
	)
	require.NoError(t, err)

	return savedItemID
}

func getEnrichmentState(
	t *testing.T,
	savedItemID uuid.UUID,
) enrichmentdbtest.GetSavedItemEnrichmentStateRow {
	t.Helper()

	db := enrichmentdbtest.New(testPool)

	state, err := db.GetSavedItemEnrichmentState(
		context.Background(),
		savedItemID,
	)
	require.NoError(t, err)

	return state
}

func metadataPtr(value string) *string {
	return &value
}

// newEnrichmentApp builds a real app wired to a controllable enricher.
func newEnrichmentApp(enricher *testutil.FakeEnricher) *fiber.App {
	app, _ := testutil.NewAppWithEnricher(testPool, enricher)

	return app
}

type enrichSavedItemBody struct {
	Data struct {
		SavedItem struct {
			ID               string  `json:"id"`
			URL              string  `json:"url"`
			Domain           *string `json:"domain"`
			Platform         *string `json:"platform"`
			Title            *string `json:"title"`
			Description      *string `json:"description"`
			ImageURL         *string `json:"image_url"`
			CollectionID     string  `json:"collection_id"`
			EnrichmentStatus string  `json:"enrichment_status"`
			LastEnrichedAt   *string `json:"last_enriched_at"`
		} `json:"saved_item"`
	} `json:"data"`
	Message string `json:"message"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// enrichSavedItem calls the endpoint as the given user and decodes the response.
func enrichSavedItem(
	t *testing.T,
	app *fiber.App,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) (*http.Response, enrichSavedItemBody) {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/saved-items/"+savedItemID.String()+"/enrich",
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	return doEnrichRequest(t, app, req)
}

func doEnrichRequest(
	t *testing.T,
	app *fiber.App,
	req *http.Request,
) (*http.Response, enrichSavedItemBody) {
	t.Helper()

	resp, err := app.Test(req)
	require.NoError(t, err)

	var body enrichSavedItemBody

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	return resp, body
}

func TestEnrichmentAPI_Enrich(t *testing.T) {
	t.Run("persists extracted metadata and marks the item completed", func(t *testing.T) {
		truncateEnrichmentData(t)

		userID := createEnrichmentUser(t, "enrich-success@example.com")
		collectionID := createUnsortedForEnrichment(t, userID)
		savedItemID := createPendingSavedItem(
			t,
			userID,
			collectionID,
			"https://example.com/articles/1",
		)

		before := getEnrichmentState(t, savedItemID)
		require.Equal(t, "pending", before.EnrichmentStatus)
		require.False(t, before.LastEnrichedAt.Valid)
		require.False(t, before.Title.Valid)

		enricher := &testutil.FakeEnricher{
			Metadata: enrichmentcore.Metadata{
				Title:       metadataPtr("An interesting article"),
				Platform:    metadataPtr("example"),
				Description: metadataPtr("A short summary"),
				ImageURL:    metadataPtr("https://example.com/og.png"),
			},
		}

		resp, body := enrichSavedItem(
			t,
			newEnrichmentApp(enricher),
			userID,
			savedItemID,
		)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		// The persisted row is the assertion that matters. The body is only
		// checked for agreeing with it.
		after := getEnrichmentState(t, savedItemID)
		require.Equal(t, "An interesting article", after.Title.String)
		require.Equal(t, "example", after.Platform.String)
		require.Equal(t, "A short summary", after.Description.String)
		require.Equal(t, "https://example.com/og.png", after.ImageUrl.String)
		require.Equal(t, "completed", after.EnrichmentStatus)
		require.True(t, after.LastEnrichedAt.Valid)

		// The URL and the domain derived at save time are untouched.
		require.Equal(t, "https://example.com/articles/1", after.Url)
		require.Equal(t, "example.com", after.Domain.String)

		require.Equal(t, "An interesting article", *body.Data.SavedItem.Title)
		require.Equal(t, "completed", body.Data.SavedItem.EnrichmentStatus)
		require.NotNil(t, body.Data.SavedItem.LastEnrichedAt)

		// The enricher was given the saved item's URL, once, and nothing else.
		require.Equal(
			t,
			[]string{"https://example.com/articles/1"},
			enricher.RequestedURLs(),
		)
	})

	t.Run("enriches with only the metadata the page exposed", func(t *testing.T) {
		truncateEnrichmentData(t)

		userID := createEnrichmentUser(t, "enrich-partial@example.com")
		collectionID := createUnsortedForEnrichment(t, userID)
		savedItemID := createPendingSavedItem(
			t,
			userID,
			collectionID,
			"https://example.com/bare",
		)

		resp, body := enrichSavedItem(
			t,
			newEnrichmentApp(&testutil.FakeEnricher{
				Metadata: enrichmentcore.Metadata{
					Title: metadataPtr("Only a title"),
				},
			}),
			userID,
			savedItemID,
		)

		// A page with one field is a successful enrichment, not a partial failure.
		require.Equal(t, http.StatusOK, resp.StatusCode)

		after := getEnrichmentState(t, savedItemID)
		require.Equal(t, "Only a title", after.Title.String)
		require.Equal(t, "completed", after.EnrichmentStatus)
		require.True(t, after.LastEnrichedAt.Valid)

		// Everything the page did not provide stays SQL NULL rather than becoming
		// an empty string. This is what "nothing was invented" means at the
		// storage layer.
		require.False(t, after.Platform.Valid)
		require.False(t, after.Description.Valid)
		require.False(t, after.ImageUrl.Valid)

		require.Nil(t, body.Data.SavedItem.Platform)
		require.Nil(t, body.Data.SavedItem.Description)
		require.Nil(t, body.Data.SavedItem.ImageURL)
	})

	t.Run("a page with no metadata at all is still completed", func(t *testing.T) {
		truncateEnrichmentData(t)

		userID := createEnrichmentUser(t, "enrich-empty@example.com")
		collectionID := createUnsortedForEnrichment(t, userID)
		savedItemID := createPendingSavedItem(
			t,
			userID,
			collectionID,
			"https://example.com/empty",
		)

		resp, _ := enrichSavedItem(
			t,
			newEnrichmentApp(&testutil.FakeEnricher{
				Metadata: enrichmentcore.Metadata{},
			}),
			userID,
			savedItemID,
		)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		after := getEnrichmentState(t, savedItemID)
		require.Equal(t, "completed", after.EnrichmentStatus)
		require.True(t, after.LastEnrichedAt.Valid)
		require.False(t, after.Title.Valid)
	})

	t.Run("never infers metadata the enricher did not provide", func(t *testing.T) {
		truncateEnrichmentData(t)

		userID := createEnrichmentUser(t, "enrich-noinfer@example.com")
		collectionID := createUnsortedForEnrichment(t, userID)

		// A recognisable hostname with no metadata. Platform must stay NULL: it is
		// derived from what the page says about itself, never from the URL.
		savedItemID := createPendingSavedItem(
			t,
			userID,
			collectionID,
			"https://www.youtube.com/watch?v=abc123",
		)

		resp, _ := enrichSavedItem(
			t,
			newEnrichmentApp(&testutil.FakeEnricher{}),
			userID,
			savedItemID,
		)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		after := getEnrichmentState(t, savedItemID)
		require.False(
			t,
			after.Platform.Valid,
			"platform must never be inferred from the hostname",
		)
		require.Equal(t, "completed", after.EnrichmentStatus)
	})

	t.Run("uses the enricher it was given rather than building one", func(t *testing.T) {
		// The enricher is injected, not constructed, so being able to swap it is
		// the whole wiring contract.
		truncateEnrichmentData(t)

		userID := createEnrichmentUser(t, "enrich-injected@example.com")
		collectionID := createUnsortedForEnrichment(t, userID)
		savedItemID := createPendingSavedItem(
			t,
			userID,
			collectionID,
			"https://example.com/injected",
		)

		enricher := &testutil.FakeEnricher{
			Metadata: enrichmentcore.Metadata{
				Title: metadataPtr("From the injected enricher"),
			},
		}

		_, _ = enrichSavedItem(
			t,
			newEnrichmentApp(enricher),
			userID,
			savedItemID,
		)

		require.Len(t, enricher.RequestedURLs(), 1)

		after := getEnrichmentState(t, savedItemID)
		require.Equal(t, "From the injected enricher", after.Title.String)
	})
}

func TestEnrichmentAPI_EnrichFailure(t *testing.T) {
	t.Run("records the failure and leaves the saved item usable", func(t *testing.T) {
		truncateEnrichmentData(t)

		userID := createEnrichmentUser(t, "enrich-failure@example.com")
		collectionID := createUnsortedForEnrichment(t, userID)
		savedItemID := createPendingSavedItem(
			t,
			userID,
			collectionID,
			"https://example.com/gone",
		)

		resp, body := enrichSavedItem(
			t,
			newEnrichmentApp(&testutil.FakeEnricher{
				Err: enrichmentcore.ErrFetchFailed,
			}),
			userID,
			savedItemID,
		)

		// The attempt completed, one way or the other. A 5xx here would tell the
		// client the saved item is broken, which it is not.
		require.Equal(t, http.StatusOK, resp.StatusCode)

		after := getEnrichmentState(t, savedItemID)
		require.Equal(t, "failed", after.EnrichmentStatus)
		require.Equal(t, "failed", body.Data.SavedItem.EnrichmentStatus)

		// Nothing about the item is invalid. The URL and its derived domain are
		// untouched, and it is still in its collection.
		require.Equal(t, "https://example.com/gone", after.Url)
		require.Equal(t, "example.com", after.Domain.String)
		require.Equal(t, collectionID, after.CollectionID)

		// A failed attempt refreshed nothing, so last_enriched_at stays NULL. It
		// records when metadata was last refreshed, not when enrichment was last
		// tried.
		require.False(t, after.LastEnrichedAt.Valid)

		// No metadata column is written on a failure.
		require.False(t, after.Title.Valid)
		require.False(t, after.Platform.Valid)
		require.False(t, after.Description.Valid)
		require.False(t, after.ImageUrl.Valid)
	})

	t.Run("a failure leaves previously enriched metadata alone", func(t *testing.T) {
		truncateEnrichmentData(t)

		userID := createEnrichmentUser(t, "enrich-refail@example.com")
		collectionID := createUnsortedForEnrichment(t, userID)
		savedItemID := createPendingSavedItem(
			t,
			userID,
			collectionID,
			"https://example.com/flaky",
		)

		_, _ = enrichSavedItem(
			t,
			newEnrichmentApp(&testutil.FakeEnricher{
				Metadata: enrichmentcore.Metadata{
					Title: metadataPtr("Found the first time"),
				},
			}),
			userID,
			savedItemID,
		)

		afterFirst := getEnrichmentState(t, savedItemID)
		require.Equal(t, "Found the first time", afterFirst.Title.String)
		require.Equal(t, "completed", afterFirst.EnrichmentStatus)
		require.True(t, afterFirst.LastEnrichedAt.Valid)

		// A later attempt fails. Metadata already on the item is not cleared by a
		// failure, because the failure says nothing about whether the title found
		// earlier is still true.
		_, _ = enrichSavedItem(
			t,
			newEnrichmentApp(&testutil.FakeEnricher{
				Err: enrichmentcore.ErrFetchFailed,
			}),
			userID,
			savedItemID,
		)

		afterSecond := getEnrichmentState(t, savedItemID)
		require.Equal(t, "failed", afterSecond.EnrichmentStatus)
		require.Equal(t, "Found the first time", afterSecond.Title.String)
		require.True(
			t,
			afterSecond.LastEnrichedAt.Valid,
			"a failed attempt must not clear last_enriched_at",
		)
	})

	t.Run("the response discloses nothing about the failure internals", func(t *testing.T) {
		// The schema has no column for why an enrichment failed and this feature
		// did not invent one, so the response reports the status and nothing else.
		// No upstream error text means nothing to probe the server with.
		truncateEnrichmentData(t)

		userID := createEnrichmentUser(t, "enrich-noreason@example.com")
		collectionID := createUnsortedForEnrichment(t, userID)
		savedItemID := createPendingSavedItem(
			t,
			userID,
			collectionID,
			"https://example.com/unexplained",
		)

		_, body := enrichSavedItem(
			t,
			newEnrichmentApp(&testutil.FakeEnricher{
				Err: enrichmentcore.ErrUnsupportedContentType,
			}),
			userID,
			savedItemID,
		)

		after := getEnrichmentState(t, savedItemID)
		require.Equal(t, "failed", after.EnrichmentStatus)

		require.Equal(t, "failed", body.Data.SavedItem.EnrichmentStatus)
		require.NotContains(t, body.Message, "content type")
		require.NotContains(t, body.Message, "dial")
		require.NotContains(t, body.Message, "169.254")
	})
}

func TestEnrichmentAPI_EnrichLastEnrichedAt(t *testing.T) {
	truncateEnrichmentData(t)

	userID := createEnrichmentUser(t, "enrich-timestamp@example.com")
	collectionID := createUnsortedForEnrichment(t, userID)
	savedItemID := createPendingSavedItem(
		t,
		userID,
		collectionID,
		"https://example.com/stamped",
	)

	_, _ = enrichSavedItem(
		t,
		newEnrichmentApp(&testutil.FakeEnricher{
			Metadata: enrichmentcore.Metadata{
				Title: metadataPtr("First"),
			},
		}),
		userID,
		savedItemID,
	)

	first := getEnrichmentState(t, savedItemID)
	require.True(t, first.LastEnrichedAt.Valid)

	_, _ = enrichSavedItem(
		t,
		newEnrichmentApp(&testutil.FakeEnricher{
			Metadata: enrichmentcore.Metadata{
				Title: metadataPtr("Second"),
			},
		}),
		userID,
		savedItemID,
	)

	second := getEnrichmentState(t, savedItemID)
	require.True(t, second.LastEnrichedAt.Valid)
	require.False(
		t,
		second.LastEnrichedAt.Time.Before(first.LastEnrichedAt.Time),
		"last_enriched_at must not move backwards",
	)
	require.Equal(t, "Second", second.Title.String)

	// updated_at moves because the row changed. This is a side effect of the
	// saved_items_set_updated_at trigger firing on any UPDATE, recorded rather
	// than worked around.
	require.False(t, second.UpdatedAt.Time.Before(first.UpdatedAt.Time))
}

func TestEnrichmentAPI_EnrichDoesNotOrganize(t *testing.T) {
	truncateEnrichmentData(t)

	userID := createEnrichmentUser(t, "enrich-nocollection@example.com")

	// The item starts in a user collection, not Unsorted, so a move would be
	// visible.
	readingList := createUserCollectionForEnrichment(t, userID, "Reading")
	unsorted := createUnsortedForEnrichment(t, userID)

	savedItemID := createPendingSavedItem(
		t,
		userID,
		readingList,
		"https://example.com/filed",
	)

	before := getEnrichmentState(t, savedItemID)
	require.Equal(t, readingList, before.CollectionID)
	require.NotEqual(t, unsorted, before.CollectionID)

	_, _ = enrichSavedItem(
		t,
		newEnrichmentApp(&testutil.FakeEnricher{
			Metadata: enrichmentcore.Metadata{
				Title:    metadataPtr("A title"),
				Platform: metadataPtr("example"),
			},
		}),
		userID,
		savedItemID,
	)

	after := getEnrichmentState(t, savedItemID)
	require.Equal(
		t,
		readingList,
		after.CollectionID,
		"enrichment must never change which collection an item is in",
	)
}

func TestEnrichmentAPI_EnrichOwnership(t *testing.T) {
	t.Run("a saved item owned by another user is not found", func(t *testing.T) {
		truncateEnrichmentData(t)

		ownerID := createEnrichmentUser(t, "enrich-owner@example.com")
		otherID := createEnrichmentUser(t, "enrich-other@example.com")

		ownerCollection := createUnsortedForEnrichment(t, ownerID)
		savedItemID := createPendingSavedItem(
			t,
			ownerID,
			ownerCollection,
			"https://example.com/private",
		)

		enricher := &testutil.FakeEnricher{
			Metadata: enrichmentcore.Metadata{
				Title: metadataPtr("Should not be found"),
			},
		}
		app := newEnrichmentApp(enricher)

		resp, body := enrichSavedItem(t, app, otherID, savedItemID)

		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		require.Equal(t, "SAVED_ITEM_NOT_FOUND", body.Error.Code)

		// The item was never fetched, so nothing about it changed.
		require.Empty(t, enricher.RequestedURLs())

		after := getEnrichmentState(t, savedItemID)
		require.Equal(t, "pending", after.EnrichmentStatus)
		require.False(t, after.Title.Valid)
	})

	t.Run("a missing saved item is not found", func(t *testing.T) {
		truncateEnrichmentData(t)

		userID := createEnrichmentUser(t, "enrich-missing@example.com")

		enricher := &testutil.FakeEnricher{}
		app := newEnrichmentApp(enricher)

		resp, body := enrichSavedItem(
			t,
			app,
			userID,
			mustParseUUID(t, "01a0f359-093b-737a-963a-80f7ca6768ed"),
		)

		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		require.Equal(t, "SAVED_ITEM_NOT_FOUND", body.Error.Code)
		require.Empty(t, enricher.RequestedURLs())
	})

	t.Run("a missing and a foreign saved item are indistinguishable", func(t *testing.T) {
		// Non-disclosure: the two cases must not be tellable apart, or the endpoint
		// becomes an existence oracle for UUIDs.
		truncateEnrichmentData(t)

		ownerID := createEnrichmentUser(t, "enrich-nd-owner@example.com")
		otherID := createEnrichmentUser(t, "enrich-nd-other@example.com")

		ownerCollection := createUnsortedForEnrichment(t, ownerID)
		foreignID := createPendingSavedItem(
			t,
			ownerID,
			ownerCollection,
			"https://example.com/foreign",
		)

		app := newEnrichmentApp(&testutil.FakeEnricher{})

		missingResp, missingBody := enrichSavedItem(
			t,
			app,
			otherID,
			mustParseUUID(t, "01a0f359-093b-737a-963a-80f7ca6768ed"),
		)

		foreignResp, foreignBody := enrichSavedItem(
			t,
			app,
			otherID,
			foreignID,
		)

		require.Equal(t, missingResp.StatusCode, foreignResp.StatusCode)
		require.Equal(t, missingBody.Error, foreignBody.Error)
	})
}

func TestEnrichmentAPI_EnrichRequestErrors(t *testing.T) {
	t.Run("an invalid saved item id is a bad request", func(t *testing.T) {
		truncateEnrichmentData(t)

		userID := createEnrichmentUser(t, "enrich-badid@example.com")

		enricher := &testutil.FakeEnricher{}
		app := newEnrichmentApp(enricher)

		req := httptest.NewRequest(
			http.MethodPost,
			"/saved-items/not-a-uuid/enrich",
			nil,
		)
		req.Header.Set(
			"Authorization",
			"Bearer "+newTestAccessToken(t, userID),
		)

		resp, _ := doEnrichRequest(t, app, req)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		require.Empty(t, enricher.RequestedURLs())
	})

	t.Run("a request without a token is unauthorized", func(t *testing.T) {
		truncateEnrichmentData(t)

		userID := createEnrichmentUser(t, "enrich-noauth@example.com")
		collectionID := createUnsortedForEnrichment(t, userID)
		savedItemID := createPendingSavedItem(
			t,
			userID,
			collectionID,
			"https://example.com/guarded",
		)

		enricher := &testutil.FakeEnricher{}
		app := newEnrichmentApp(enricher)

		req := httptest.NewRequest(
			http.MethodPost,
			"/saved-items/"+savedItemID.String()+"/enrich",
			nil,
		)

		resp, _ := doEnrichRequest(t, app, req)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		require.Empty(t, enricher.RequestedURLs())

		after := getEnrichmentState(t, savedItemID)
		require.Equal(
			t,
			"pending",
			after.EnrichmentStatus,
			"an unauthenticated request must not enrich anything",
		)
	})
}
