package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/search"
)

// runSearch drives GET /search end to end, through the real app, middleware,
// handler, service and repository.
//
// rawQuery is the query value as the caller means it, including any literal
// whitespace; it is URL encoded here so a padded query can be sent as one. An
// empty rawQuery sends no q parameter at all, which is the absent-parameter case.
func runSearch(
	t *testing.T,
	userID uuid.UUID,
	rawQuery string,
	authorize bool,
) *http.Response {
	t.Helper()

	target := "/search"
	if rawQuery != "" {
		target += "?q=" + url.QueryEscape(rawQuery)
	}

	req := httptest.NewRequest(http.MethodGet, target, nil)

	if authorize {
		req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))
	}

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	return resp
}

type searchAPIResponse struct {
	Data struct {
		Collections []struct {
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			Type      string  `json:"type"`
			SystemKey *string `json:"system_key"`
			CreatedAt string  `json:"created_at"`
			UpdatedAt string  `json:"updated_at"`
		} `json:"collections"`
		SavedItems []struct {
			ID           string  `json:"id"`
			URL          string  `json:"url"`
			Domain       *string `json:"domain"`
			Title        *string `json:"title"`
			CollectionID string  `json:"collection_id"`
			CreatedAt    string  `json:"created_at"`
			UpdatedAt    string  `json:"updated_at"`
		} `json:"saved_items"`
	} `json:"data"`
	Message string `json:"message"`
}

type searchAPIErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeSearchAPI(
	t *testing.T,
	resp *http.Response,
) searchAPIResponse {
	t.Helper()

	raw := readResponseBody(t, resp)

	var body searchAPIResponse

	require.NoError(t, json.Unmarshal([]byte(raw), &body))

	return body
}

func decodeSearchAPIError(
	t *testing.T,
	resp *http.Response,
) searchAPIErrorResponse {
	t.Helper()

	raw := readResponseBody(t, resp)

	var body searchAPIErrorResponse

	require.NoError(t, json.Unmarshal([]byte(raw), &body))

	return body
}

func savedItemIDsFromAPI(body searchAPIResponse) []string {
	ids := make([]string, 0, len(body.Data.SavedItems))

	for _, item := range body.Data.SavedItems {
		ids = append(ids, item.ID)
	}

	return ids
}

func collectionIDsFromAPI(body searchAPIResponse) []string {
	ids := make([]string, 0, len(body.Data.Collections))

	for _, collection := range body.Data.Collections {
		ids = append(ids, collection.ID)
	}

	return ids
}

func indexOfString(haystack []string, needle string) int {
	for i, candidate := range haystack {
		if candidate == needle {
			return i
		}
	}

	return -1
}

func readResponseBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return string(raw)
}

func TestSearchAPI_Search(t *testing.T) {
	t.Run("returns both result groups through HTTP", func(t *testing.T) {
		truncateSearchData(t)

		userID, _ := createSearchUserWithUnsorted(t, "search-api-both@example.com")

		gearID := createSearchCollection(t, userID, "Camera Gear", "user", "")

		savedItemID := createSearchSavedItem(
			t,
			userID,
			gearID,
			"https://example.org/notes/camera-buying-guide",
			"example.org",
			"Camera Buying Guide",
		)

		resp := runSearch(t, userID, "camera", true)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body := decodeSearchAPI(t, resp)

		require.Equal(t, "search completed successfully", body.Message)

		// Both arrays are present in one response: the user typed one query.
		require.Contains(t, savedItemIDsFromAPI(body), savedItemID.String())
		require.Contains(t, collectionIDsFromAPI(body), gearID.String())
	})

	t.Run("returns 200 with empty arrays when nothing matches", func(t *testing.T) {
		truncateSearchData(t)

		userID, unsortedID := createSearchUserWithUnsorted(t, "search-api-empty@example.com")

		createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://example.com/articles/1",
			"example.com",
			"Camera Buying Guide",
		)

		resp := runSearch(t, userID, "zzzqqq", true)

		// A search that matched nothing is a successful request, not a 404.
		require.Equal(t, http.StatusOK, resp.StatusCode)

		raw := readResponseBody(t, resp)
		require.Contains(t, raw, `"saved_items":[]`)
		require.Contains(t, raw, `"collections":[]`)

		body := decodeSearchAPIFromRaw(t, raw)
		require.Empty(t, body.Data.SavedItems)
		require.Empty(t, body.Data.Collections)
	})

	t.Run("returns the collection id for every saved item result", func(t *testing.T) {
		truncateSearchData(t)

		userID, _ := createSearchUserWithUnsorted(t, "search-api-fields@example.com")

		gearID := createSearchCollection(t, userID, "Camera Gear", "user", "")

		savedItemID := createSearchSavedItem(
			t,
			userID,
			gearID,
			"https://example.org/notes/camera-buying-guide",
			"example.org",
			"Camera Buying Guide",
		)

		resp := runSearch(t, userID, "camera", true)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body := decodeSearchAPI(t, resp)

		index := indexOfString(savedItemIDsFromAPI(body), savedItemID.String())
		require.NotEqual(t, -1, index)

		item := body.Data.SavedItems[index]

		// collection_id is the field the client most needs from a search result: it
		// is where the item currently lives.
		require.Equal(t, gearID.String(), item.CollectionID)
		require.Equal(
			t,
			"https://example.org/notes/camera-buying-guide",
			item.URL,
		)
		require.NotNil(t, item.Domain)
		require.Equal(t, "example.org", *item.Domain)
		require.NotNil(t, item.Title)
		require.Equal(t, "Camera Buying Guide", *item.Title)
		require.NotEmpty(t, item.CreatedAt)
		require.NotEmpty(t, item.UpdatedAt)

		// The database agrees the item is in that collection.
		require.Equal(
			t,
			gearID,
			savedItemState(t, savedItemID).CollectionID,
		)
	})

	t.Run("returns null for metadata nothing has populated", func(t *testing.T) {
		truncateSearchData(t)

		userID, unsortedID := createSearchUserWithUnsorted(t, "search-api-nulls@example.com")

		// Nothing populates title in production yet, so this is the real shape.
		createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://example.com/articles/1",
			"",
			"",
		)

		resp := runSearch(t, userID, "articles", true)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		raw := readResponseBody(t, resp)

		// Present and explicitly null, not dropped and not an empty string.
		require.Contains(t, raw, `"title":null`)
		require.Contains(t, raw, `"domain":null`)

		// Platform is not a searchable field and is never part of a result.
		require.NotContains(t, raw, "platform")

		// Enrichment is not read or reported by search.
		require.NotContains(t, raw, "enrichment")
		require.NotContains(t, raw, "last_enriched")
	})

	t.Run("matches a fuzzy typo through the whole stack", func(t *testing.T) {
		truncateSearchData(t)

		userID, unsortedID := createSearchUserWithUnsorted(t, "search-api-fuzzy@example.com")

		savedItemID := createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://sourdough.example/guides/bread",
			"sourdough.example",
			"",
		)

		resp := runSearch(t, userID, "sourdogh", true)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body := decodeSearchAPI(t, resp)

		// No field contains the typo verbatim, so this result can only have come
		// from the fuzzy fallback inside the one query.
		require.Contains(t, savedItemIDsFromAPI(body), savedItemID.String())
	})

	t.Run("does not expose the relevance score in the response", func(t *testing.T) {
		truncateSearchData(t)

		userID, unsortedID := createSearchUserWithUnsorted(t, "search-api-noscore@example.com")

		createSearchCollection(t, userID, "Camera Gear", "user", "")

		createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://example.org/notes/camera-buying-guide",
			"example.org",
			"Camera Buying Guide",
		)

		resp := runSearch(t, userID, "camera", true)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		raw := readResponseBody(t, resp)

		// Ordering is the contract, not the number behind it. The score exists only
		// so the search SQL can rank rows, and it must not leak into the payload.
		require.NotContains(t, raw, "score")

		// Everything else in the contract is still there.
		body := decodeSearchAPIFromRaw(t, raw)
		require.NotEmpty(t, body.Data.SavedItems)
		require.NotEmpty(t, body.Data.Collections)
		require.NotEmpty(t, body.Data.SavedItems[0].CollectionID)
	})

	t.Run("finds the Unsorted system collection", func(t *testing.T) {
		truncateSearchData(t)

		userID, unsortedID := createSearchUserWithUnsorted(t, "search-api-system@example.com")

		resp := runSearch(t, userID, "unsorted", true)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body := decodeSearchAPI(t, resp)

		ids := collectionIDsFromAPI(body)
		require.Contains(t, ids, unsortedID.String())

		index := indexOfString(ids, unsortedID.String())
		found := body.Data.Collections[index]

		// System collections are searchable, and the client can tell them apart.
		require.Equal(t, "Unsorted", found.Name)
		require.Equal(t, "system", found.Type)
		require.NotNil(t, found.SystemKey)
		require.Equal(t, "unsorted", *found.SystemKey)
	})

	t.Run("ranks a substring match above a fuzzy-only match through HTTP", func(t *testing.T) {
		truncateSearchData(t)

		userID, unsortedID := createSearchUserWithUnsorted(t, "search-api-rank@example.com")

		substringMatchID := createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://example.com/sourdough-guide",
			"example.com",
			"",
		)
		fuzzyMatchID := createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://recipecard.io/sourdogh-starter",
			"recipecard.io",
			"",
		)

		resp := runSearch(t, userID, "sourdough", true)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body := decodeSearchAPI(t, resp)
		ids := savedItemIDsFromAPI(body)

		require.Contains(t, ids, substringMatchID.String())
		require.Contains(t, ids, fuzzyMatchID.String())

		// Asserted as an ordering, not as a score value.
		require.Less(
			t,
			indexOfString(ids, substringMatchID.String()),
			indexOfString(ids, fuzzyMatchID.String()),
		)
	})
}

func TestSearchAPI_UserIsolation(t *testing.T) {
	t.Run("returns only the authenticated user's results through HTTP", func(t *testing.T) {
		truncateSearchData(t)

		ownerID, ownerUnsorted := createSearchUserWithUnsorted(t, "search-api-owner@example.com")
		otherID, otherUnsorted := createSearchUserWithUnsorted(t, "search-api-other@example.com")

		// Both users own matching data with byte identical searchable text.
		ownerItemID := createSearchSavedItem(
			t,
			ownerID,
			ownerUnsorted,
			"https://shared.example/cameras",
			"shared.example",
			"Camera Gear Guide",
		)
		otherItemID := createSearchSavedItem(
			t,
			otherID,
			otherUnsorted,
			"https://shared.example/cameras",
			"shared.example",
			"Camera Gear Guide",
		)
		require.NotEqual(t, ownerItemID, otherItemID)

		ownerCollectionID := createSearchCollection(t, ownerID, "Camera Gear", "user", "")
		otherCollectionID := createSearchCollection(t, otherID, "Camera Gear", "user", "")
		require.NotEqual(t, ownerCollectionID, otherCollectionID)

		// Authenticate as the owner. The token is the only identity the request has.
		resp := runSearch(t, ownerID, "Camera Gear", true)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body := decodeSearchAPI(t, resp)

		ids := savedItemIDsFromAPI(body)
		require.Contains(t, ids, ownerItemID.String())
		require.NotContains(t, ids, otherItemID.String())

		collectionIDs := collectionIDsFromAPI(body)
		require.Contains(t, collectionIDs, ownerCollectionID.String())
		require.NotContains(t, collectionIDs, otherCollectionID.String())

		// The other user's saved item is still in the other user's collection, so
		// nothing was moved or rewritten by being searched.
		require.Equal(t, otherUnsorted, savedItemState(t, otherItemID).CollectionID)
		require.Equal(t, ownerUnsorted, savedItemState(t, ownerItemID).CollectionID)
	})

	t.Run("ignores a user_id supplied in the query string", func(t *testing.T) {
		truncateSearchData(t)

		ownerID, _ := createSearchUserWithUnsorted(t, "search-api-inject@example.com")
		otherID, otherUnsorted := createSearchUserWithUnsorted(t, "search-api-inject-other@example.com")

		otherItemID := createSearchSavedItem(
			t,
			otherID,
			otherUnsorted,
			"https://shared.example/cameras",
			"shared.example",
			"",
		)

		req := httptest.NewRequest(
			http.MethodGet,
			"/search?q=cameras&user_id="+otherID.String(),
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, ownerID))

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body := decodeSearchAPI(t, resp)

		// The authenticated user is always the search owner. A user_id in the query
		// string is not an identity and must not become one.
		require.NotContains(t, savedItemIDsFromAPI(body), otherItemID.String())
	})
}

func TestSearchAPI_Validation(t *testing.T) {
	t.Run("returns 400 for a query that is too short", func(t *testing.T) {
		truncateSearchData(t)

		userID, _ := createSearchUserWithUnsorted(t, "search-api-short@example.com")

		resp := runSearch(t, userID, "a", true)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		// The existing error response shape, carrying the service's own code.
		body := decodeSearchAPIError(t, resp)
		require.Equal(t, search.CodeInvalidSearchQuery, body.Error.Code)
		require.NotEmpty(t, body.Error.Message)
	})

	t.Run("returns 400 for a query that is too long", func(t *testing.T) {
		truncateSearchData(t)

		userID, _ := createSearchUserWithUnsorted(t, "search-api-long@example.com")

		tooLong := strings.Repeat("a", 129)
		require.Equal(t, 129, len([]rune(tooLong)))

		resp := runSearch(t, userID, tooLong, true)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		body := decodeSearchAPIError(t, resp)
		require.Equal(t, search.CodeInvalidSearchQuery, body.Error.Code)
	})

	t.Run("accepts a query at the maximum length", func(t *testing.T) {
		truncateSearchData(t)

		userID, _ := createSearchUserWithUnsorted(t, "search-api-atmax@example.com")

		atMaximum := strings.Repeat("a", 128)
		require.Equal(t, 128, len([]rune(atMaximum)))

		resp := runSearch(t, userID, atMaximum, true)

		// 128 runes is inside the bound, so this is a successful empty search.
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("returns 400 for an absent query parameter", func(t *testing.T) {
		truncateSearchData(t)

		userID, _ := createSearchUserWithUnsorted(t, "search-api-absent@example.com")

		resp := runSearch(t, userID, "", true)

		// A missing q is an empty query, which the service rejects as blank.
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		body := decodeSearchAPIError(t, resp)
		require.Equal(t, search.CodeInvalidSearchQuery, body.Error.Code)
	})

	t.Run("returns 400 for a whitespace-only query", func(t *testing.T) {
		truncateSearchData(t)

		userID, _ := createSearchUserWithUnsorted(t, "search-api-ws@example.com")

		// A whitespace-only query. The service trims it and then rejects what is
		// left, so this exercises the approved normalization through HTTP without
		// restating the rule.
		resp := runSearch(t, userID, "   ", true)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		body := decodeSearchAPIError(t, resp)
		require.Equal(t, search.CodeInvalidSearchQuery, body.Error.Code)
	})

	t.Run("trims a padded query and searches the trimmed value", func(t *testing.T) {
		truncateSearchData(t)

		userID, unsortedID := createSearchUserWithUnsorted(t, "search-api-pad@example.com")

		savedItemID := createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://example.org/cameras",
			"example.org",
			"",
		)

		resp := runSearch(t, userID, "  camera  ", true)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body := decodeSearchAPI(t, resp)

		// The padded query normalized to "camera" and matched, so the trimming
		// happened in the service rather than being required of the caller.
		require.Contains(t, savedItemIDsFromAPI(body), savedItemID.String())
	})
}

func TestSearchAPI_Authentication(t *testing.T) {
	t.Run("returns 401 without an access token", func(t *testing.T) {
		truncateSearchData(t)

		resp := runSearch(t, uuid.New(), "camera", false)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		// The existing unauthorized response shape.
		body := decodeSearchAPIError(t, resp)
		require.Equal(t, "INVALID_AUTHORIZATION_HEADER", body.Error.Code)
	})

	t.Run("returns 401 for an invalid access token", func(t *testing.T) {
		truncateSearchData(t)

		req := httptest.NewRequest(http.MethodGet, "/search?q=camera", nil)
		req.Header.Set("Authorization", "Bearer not-a-real-token")

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		body := decodeSearchAPIError(t, resp)
		require.Equal(t, "INVALID_ACCESS_TOKEN", body.Error.Code)
	})

	t.Run("is not reachable as a public route", func(t *testing.T) {
		truncateSearchData(t)

		// No shape of the path leaks a public search.
		targets := []string{"/search", "/search/", "/search?q=camera"}

		for _, target := range targets {
			resp, err := testApp.Test(
				httptest.NewRequest(http.MethodGet, target, nil),
			)
			require.NoError(t, err)

			require.Equal(
				t,
				http.StatusUnauthorized,
				resp.StatusCode,
				"unexpected status for %s: %s",
				target,
				readResponseBody(t, resp),
			)
		}
	})
}

// decodeSearchAPIFromRaw parses a body that has already been read.
func decodeSearchAPIFromRaw(t *testing.T, raw string) searchAPIResponse {
	t.Helper()

	var body searchAPIResponse

	require.NoError(t, json.Unmarshal([]byte(raw), &body))

	return body
}
