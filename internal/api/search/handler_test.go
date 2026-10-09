package search_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/search"
	searchmocks "github.com/thoriqr/stash-it-backend/internal/api/search/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/validation"
)

const handlerTestAccessTokenSecret = "search-handler-test-secret"

// newHandlerTestApp builds the real handler, wired to a mock repository, behind the
// real auth middleware. Binding, status codes, response shape and error
// pass-through are all exercised; only the repository is substituted, because it
// needs a real database and is covered by the integration tests instead.
func newHandlerTestApp(
	t *testing.T,
	repo search.Repository,
) *fiber.App {
	t.Helper()

	app := fiber.New(fiber.Config{
		ErrorHandler:    httpx.NewErrorHandler(zap.NewNop()),
		StructValidator: validation.New(),
	})

	search.Routes(
		app.Group("/search"),
		search.NewHandler(search.NewService(repo)),
		security.NewAccessTokenVerifier([]byte(handlerTestAccessTokenSecret)),
	)

	return app
}

func handlerTestToken(t *testing.T, userID uuid.UUID) string {
	t.Helper()

	token, err := security.NewAccessTokenGenerator(
		[]byte(handlerTestAccessTokenSecret),
	).Generate(userID, uuid.New(), time.Hour)
	require.NoError(t, err)

	return token
}

// searchRequest builds a GET /search request. rawQuery is placed verbatim after
// "q=" so a test can construct a padded or absent query without the handler
// normalising it on the way in.
func searchRequest(
	t *testing.T,
	userID uuid.UUID,
	rawQuery string,
	authorize bool,
) *http.Request {
	t.Helper()

	target := "/search"
	if rawQuery != "" {
		target += "?q=" + rawQuery
	}

	req := httptest.NewRequest(http.MethodGet, target, nil)

	if authorize {
		req.Header.Set(
			"Authorization",
			"Bearer "+handlerTestToken(t, userID),
		)
	}

	return req
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return string(raw)
}

// searchPayload mirrors the wire shape of a search response, including which
// fields are nullable. It is decoded here rather than reusing the API response
// types so these tests assert what actually reaches a client.
type searchPayload struct {
	Message string `json:"message"`
	Data    struct {
		Collections []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"collections"`
		SavedItems []struct {
			ID               string  `json:"id"`
			Title            *string `json:"title"`
			URL              string  `json:"url"`
			Domain           *string `json:"domain"`
			ImageURL         *string `json:"image_url"`
			EnrichmentStatus string  `json:"enrichment_status"`
			Collection       struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"collection"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"saved_items"`
	} `json:"data"`
}

func decodeSearchPayload(t *testing.T, resp *http.Response) searchPayload {
	t.Helper()

	var payload searchPayload

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))

	return payload
}

// decodeSearchPayloadAndRaw decodes the payload and also returns the raw body.
// A response body can only be read once, so a test that wants both the parsed
// shape and the exact JSON must take them together.
func decodeSearchPayloadAndRaw(
	t *testing.T,
	resp *http.Response,
) (searchPayload, string) {
	t.Helper()

	raw := readBody(t, resp)

	return decodeSearchAPIFromString(t, raw), raw
}

// decodeSearchAPIFromString parses a body that has already been read.
func decodeSearchAPIFromString(t *testing.T, raw string) searchPayload {
	t.Helper()

	var payload searchPayload

	require.NoError(t, json.Unmarshal([]byte(raw), &payload))

	return payload
}

// firstObjectField walks a path of object keys and returns the first element of the
// array found at the end of it.
//
// Decoding into the typed payload cannot answer what a response does not declare:
// an undeclared key is simply left out of the struct. These tests assert exact key
// sets, so they read the body as plain JSON.
func firstObjectField(
	t *testing.T,
	raw string,
	path ...string,
) map[string]any {
	t.Helper()

	var current any

	require.NoError(t, json.Unmarshal([]byte(raw), &current))

	for _, key := range path {
		object, ok := current.(map[string]any)
		require.True(t, ok, "expected an object at %q", key)

		current, ok = object[key]
		require.True(t, ok, "response has no %q", key)
	}

	elements, ok := current.([]any)
	require.True(t, ok, "expected an array at %q", path[len(path)-1])
	require.NotEmpty(t, elements)

	element, ok := elements[0].(map[string]any)
	require.True(t, ok, "expected an object in the array at %q", path[len(path)-1])

	return element
}

// objectFieldKeys lists what a decoded object carries, so a test can pin the exact
// set rather than spot-check the fields it happens to remember.
func objectFieldKeys(t *testing.T, object map[string]any) []string {
	t.Helper()

	keys := make([]string, 0, len(object))

	for key := range object {
		keys = append(keys, key)
	}

	return keys
}

func handlerTimestamptz() pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
}

func handlerPgText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}

func TestHandler_Search(t *testing.T) {
	t.Run("returns both result groups and returns 200", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		userID := uuid.New()
		savedItemID := uuid.New()
		collectionID := uuid.New()

		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Return([]search.SearchSavedItem{{
				ID:               savedItemID,
				Title:            handlerPgText("Camera Buying Guide"),
				URL:              "https://example.org/cameras",
				Domain:           handlerPgText("example.org"),
				ImageURL:         handlerPgText("https://example.org/og.png"),
				EnrichmentStatus: "completed",
				Collection: search.SearchCollectionRef{
					ID:   collectionID,
					Name: "Camera Gear",
				},
				CreatedAt: handlerTimestamptz(),
				Score:     10.95,
			}}, nil)

		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return([]search.SearchCollection{{
				ID:    collectionID,
				Name:  "Camera Gear",
				Score: 10.95,
			}}, nil)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(searchRequest(t, userID, "camera", true))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		payload := decodeSearchPayload(t, resp)

		require.Equal(t, "search completed successfully", payload.Message)
		require.Len(t, payload.Data.SavedItems, 1)
		require.Len(t, payload.Data.Collections, 1)

		item := payload.Data.SavedItems[0]

		require.Equal(t, savedItemID.String(), item.ID)
		require.Equal(t, "https://example.org/cameras", item.URL)
		require.Equal(t, "example.org", *item.Domain)
		require.Equal(t, "Camera Buying Guide", *item.Title)
		require.Equal(t, "https://example.org/og.png", *item.ImageURL)
		require.Equal(t, "completed", item.EnrichmentStatus)
		require.False(t, item.CreatedAt.IsZero())

		// The collection is reported as an object, so the client can both show
		// where the result lives and name it without a second request.
		require.Equal(t, collectionID.String(), item.Collection.ID)
		require.Equal(t, "Camera Gear", item.Collection.Name)

		require.Equal(t, collectionID.String(), payload.Data.Collections[0].ID)
		require.Equal(t, "Camera Gear", payload.Data.Collections[0].Name)
	})

	t.Run("returns 200 with empty arrays when nothing matches", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		userID := uuid.New()

		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Return([]search.SearchSavedItem{}, nil)
		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return([]search.SearchCollection{}, nil)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(searchRequest(t, userID, "zzzqqq", true))
		require.NoError(t, err)

		// An empty search is a successful request, not a missing resource.
		require.Equal(t, http.StatusOK, resp.StatusCode)

		payload, raw := decodeSearchPayloadAndRaw(t, resp)
		require.Empty(t, payload.Data.SavedItems)
		require.Empty(t, payload.Data.Collections)

		// Both keys must be present and be arrays, not null and not absent.
		require.Contains(t, raw, `"saved_items":[]`)
		require.Contains(t, raw, `"collections":[]`)
	})

	t.Run("does not reorder the results the service produced", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		userID := uuid.New()

		// A high score last. The handler must not re-sort: relevance and order are
		// decided by the search SQL.
		first := search.SearchSavedItem{ID: uuid.New(), Score: 0.5}
		second := search.SearchSavedItem{ID: uuid.New(), Score: 10.0}

		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Return([]search.SearchSavedItem{first, second}, nil)
		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return([]search.SearchCollection{}, nil)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(searchRequest(t, userID, "camera", true))
		require.NoError(t, err)

		payload := decodeSearchPayload(t, resp)
		require.Equal(t, first.ID.String(), payload.Data.SavedItems[0].ID)
		require.Equal(t, second.ID.String(), payload.Data.SavedItems[1].ID)
	})

	t.Run("passes no caller limit so the service applies its own bounds", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		userID := uuid.New()

		// There is no limit parameter on this endpoint, so the handler forwards the
		// service's default. The service applies the default and the maximum.
		repo.EXPECT().
			SearchSavedItems(
				gomock.Any(),
				search.SearchSavedItemsParams{
					UserID: userID,
					Query:  "camera",
					Limit:  search.SavedItemDefaultLimit,
				},
			).
			Return([]search.SearchSavedItem{}, nil)

		repo.EXPECT().
			SearchCollections(
				gomock.Any(),
				search.SearchCollectionsParams{
					UserID: userID,
					Query:  "camera",
					Limit:  search.CollectionLimit,
				},
			).
			Return([]search.SearchCollection{}, nil)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(searchRequest(t, userID, "camera", true))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("ignores a limit query parameter because the endpoint has none", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		userID := uuid.New()

		// A caller-supplied limit cannot widen the result: the request type has no
		// field for it, so it never reaches the service.
		repo.EXPECT().
			SearchSavedItems(
				gomock.Any(),
				search.SearchSavedItemsParams{
					UserID: userID,
					Query:  "camera",
					Limit:  search.SavedItemDefaultLimit,
				},
			).
			Return([]search.SearchSavedItem{}, nil)

		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return([]search.SearchCollection{}, nil)

		app := newHandlerTestApp(t, repo)

		req := httptest.NewRequest(
			http.MethodGet,
			"/search?q=camera&limit=5000&page=9&cursor=abc",
			nil,
		)
		req.Header.Set(
			"Authorization",
			"Bearer "+handlerTestToken(t, userID),
		)

		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("does not normalize the query in the handler", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		userID := uuid.New()

		// Trimming and the length rules belong to the service. The handler hands the
		// query over as it arrived and does no normalization of its own. The value
		// the repository sees is the already-trimmed one, which proves the trimming
		// happened in the service rather than twice.
		repo.EXPECT().
			SearchSavedItems(
				gomock.Any(),
				search.SearchSavedItemsParams{
					UserID: userID,
					Query:  "camera",
					Limit:  search.SavedItemDefaultLimit,
				},
			).
			Return([]search.SearchSavedItem{}, nil)

		repo.EXPECT().
			SearchCollections(
				gomock.Any(),
				search.SearchCollectionsParams{
					UserID: userID,
					Query:  "camera",
					Limit:  search.CollectionLimit,
				},
			).
			Return([]search.SearchCollection{}, nil)

		app := newHandlerTestApp(t, repo)

		// %20 is a space, so this arrives padded on both sides.
		resp, err := app.Test(searchRequest(t, userID, "%20%20camera%20%20", true))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("returns 400 with the search error code for a query that is too short", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		// The service rejects it, so the repository is never reached.
		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Times(0)
		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Times(0)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(searchRequest(t, uuid.New(), "a", true))
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		// The service's own code, not a generic BAD_REQUEST and not a second
		// VALIDATION_ERROR from a duplicate check in the handler.
		require.Equal(t, search.CodeInvalidSearchQuery, body.Error.Code)
	})

	t.Run("returns 400 for an absent query parameter", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Times(0)
		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Times(0)

		app := newHandlerTestApp(t, repo)

		req := httptest.NewRequest(http.MethodGet, "/search", nil)
		req.Header.Set(
			"Authorization",
			"Bearer "+handlerTestToken(t, uuid.New()),
		)

		resp, err := app.Test(req)
		require.NoError(t, err)

		// A missing q is an empty query, which the service rejects as blank.
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, search.CodeInvalidSearchQuery, body.Error.Code)
	})

	t.Run("returns 400 for a whitespace-only query", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Times(0)
		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Times(0)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(
			searchRequest(t, uuid.New(), "%20%20%20", true),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, search.CodeInvalidSearchQuery, body.Error.Code)
	})

	t.Run("returns 401 without an access token", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Times(0)
		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Times(0)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(searchRequest(t, uuid.New(), "camera", false))
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "INVALID_AUTHORIZATION_HEADER", body.Error.Code)
	})

	t.Run("returns 401 for an invalid access token", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		app := newHandlerTestApp(t, repo)

		req := searchRequest(t, uuid.New(), "camera", false)
		req.Header.Set("Authorization", "Bearer not-a-real-token")

		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "INVALID_ACCESS_TOKEN", body.Error.Code)
	})

	t.Run("takes the search owner from the token, never from the request", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		authenticatedUserID := uuid.New()

		// A user_id in the query string must not become the search owner. The
		// authenticated user is always the owner.
		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params search.SearchSavedItemsParams,
				) ([]search.SearchSavedItem, error) {
					require.Equal(t, authenticatedUserID, params.UserID)

					return nil, nil
				},
			)

		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params search.SearchCollectionsParams,
				) ([]search.SearchCollection, error) {
					require.Equal(t, authenticatedUserID, params.UserID)

					return nil, nil
				},
			)

		app := newHandlerTestApp(t, repo)

		req := httptest.NewRequest(
			http.MethodGet,
			"/search?q=camera&user_id="+uuid.New().String(),
			nil,
		)
		req.Header.Set(
			"Authorization",
			"Bearer "+handlerTestToken(t, authenticatedUserID),
		)

		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	// A system collection is searchable and is reported exactly like any other
	// one. How a collection came to be is answered by GET /collections, so a
	// search result carries no type and no system_key to branch on.
	t.Run("reports a system collection through the same contract", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		userID := uuid.New()
		unsortedID := uuid.New()

		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Return([]search.SearchSavedItem{}, nil)

		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return([]search.SearchCollection{{
				ID:    unsortedID,
				Name:  "Unsorted",
				Score: 10.95,
			}}, nil)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(searchRequest(t, userID, "unsorted", true))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		payload, raw := decodeSearchPayloadAndRaw(t, resp)
		require.Len(t, payload.Data.Collections, 1)
		require.Equal(t, unsortedID.String(), payload.Data.Collections[0].ID)
		require.Equal(t, "Unsorted", payload.Data.Collections[0].Name)

		// Only two keys, so nothing here invites a client to tell system
		// collections apart from its own by matching on a response field.
		collection := firstObjectField(t, raw, "data", "collections")
		require.ElementsMatch(t, []string{"id", "name"}, objectFieldKeys(t, collection))
	})

	t.Run("reports null metadata as JSON null rather than dropping it", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		userID := uuid.New()

		// Nothing populates title or image_url beyond what the save path already
		// wrote, so nulls are the common case and the payload shape must not
		// change.
		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Return([]search.SearchSavedItem{{
				ID:         uuid.New(),
				URL:        "https://example.com/cameras",
				Domain:     pgtype.Text{},
				Title:      pgtype.Text{},
				ImageURL:   pgtype.Text{},
				Collection: search.SearchCollectionRef{ID: uuid.New(), Name: "Unsorted"},
				// The stored value, not a constant this code assumes: an item that
				// has not been enriched says so rather than reading as an absence.
				EnrichmentStatus: "pending",
				CreatedAt:        handlerTimestamptz(),
				Score:            0.5,
			}}, nil)
		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return([]search.SearchCollection{}, nil)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(searchRequest(t, userID, "cameras", true))
		require.NoError(t, err)

		raw := readBody(t, resp)

		require.Contains(t, raw, `"domain":null`)
		require.Contains(t, raw, `"title":null`)
		require.Contains(t, raw, `"image_url":null`)

		// A title is never assembled from the domain or the url, so a null title
		// stays null rather than becoming something that was never on the page.
		require.Contains(t, raw, `"title":null,"url":"https://example.com/cameras"`)

		require.Contains(t, raw, `"enrichment_status":"pending"`)

		// Platform is not a searchable field and is never part of a result, and
		// nothing else is reported that the result cannot be rendered from.
		require.NotContains(t, raw, "platform")
		require.NotContains(t, raw, "last_enriched")
		require.NotContains(t, raw, "updated_at")
		require.NotContains(t, raw, "description")
		require.NotContains(t, raw, "system_key")
		require.NotContains(t, raw, `"type"`)
	})

	t.Run("does not expose the relevance score in the payload", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		userID := uuid.New()

		// The service still returns a score, because the search SQL needs it to
		// order the rows. It is internal to that ordering and is not part of the
		// response contract, so it must not reach the client even when present.
		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Return([]search.SearchSavedItem{{
				ID:     uuid.New(),
				Title:  handlerPgText("Camera Buying Guide"),
				URL:    "https://example.com/cameras",
				Domain: handlerPgText("example.com"),
				Collection: search.SearchCollectionRef{
					ID:   uuid.New(),
					Name: "Unsorted",
				},
				EnrichmentStatus: "completed",
				CreatedAt:        handlerTimestamptz(),
				Score:            10.95,
			}}, nil)

		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return([]search.SearchCollection{{
				ID:    uuid.New(),
				Name:  "Camera Gear",
				Score: 10.95,
			}}, nil)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(searchRequest(t, userID, "camera", true))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		raw := readBody(t, resp)

		require.NotContains(t, raw, "score")

		// The rest of the payload is untouched by removing it.
		payload := decodeSearchAPIFromString(t, raw)
		require.Len(t, payload.Data.SavedItems, 1)
		require.Len(t, payload.Data.Collections, 1)
		require.NotEmpty(t, payload.Data.SavedItems[0].Collection.ID)
	})

	t.Run("keeps the driver detail out of an internal error response", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		userID := uuid.New()

		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Return(nil, apperror.Internal(errors.New("connection reset by peer")))

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(searchRequest(t, userID, "camera", true))
		require.NoError(t, err)
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)

		// The database error text must not reach the client.
		require.NotContains(t, readBody(t, resp), "connection reset by peer")
	})

	t.Run("does nothing beyond one service call", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		userID := uuid.New()

		// The handler owns no search logic, so it composes nothing: it reads the
		// token, reads q, calls the service once and maps the result.
		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Return([]search.SearchSavedItem{}, nil).
			Times(1)
		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return([]search.SearchCollection{}, nil).
			Times(1)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(searchRequest(t, userID, "camera", true))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// assertSearchRouteIsNotPublic documents that /search is not reachable without a
// token, so there is no accidental public search.
func assertSearchRouteIsNotPublic(t *testing.T, app *fiber.App) {
	t.Helper()

	for _, target := range []string{"/search", "/search?q=camera", "/search/"} {
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, target, nil))
		require.NoError(t, err)

		require.Equal(
			t,
			http.StatusUnauthorized,
			resp.StatusCode,
			"unexpected status for %s: %s",
			target,
			strings.TrimSpace(readBody(t, resp)),
		)
	}
}

func TestHandler_SearchRouteIsAuthenticated(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := searchmocks.NewMockRepository(ctrl)

	app := newHandlerTestApp(t, repo)

	assertSearchRouteIsNotPublic(t, app)
}
