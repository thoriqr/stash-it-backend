package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/collection"
	saveditemdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/saved_item/generated"
)

// putIntoCollection drives the real HTTP endpoint end to end, through the real
// app, middleware, handler, service and repository.
func putIntoCollection(
	t *testing.T,
	userID uuid.UUID,
	savedItemID uuid.UUID,
	collectionName string,
) *http.Response {
	t.Helper()

	body, err := json.Marshal(
		map[string]string{"collection_name": collectionName},
	)
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPut,
		"/saved-items/"+savedItemID.String()+"/collection",
		strings.NewReader(string(body)),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	return resp
}

type putCollectionResponse struct {
	Data struct {
		Collection struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Type      string `json:"type"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
		} `json:"collection"`
		SavedItem struct {
			ID           string  `json:"id"`
			URL          string  `json:"url"`
			Domain       *string `json:"domain"`
			Platform     *string `json:"platform"`
			Title        *string `json:"title"`
			CollectionID string  `json:"collection_id"`
		} `json:"saved_item"`
		CollectionCreated   bool `json:"collection_created"`
		AlreadyInCollection bool `json:"already_in_collection"`
	} `json:"data"`
	Message string `json:"message"`
}

func TestCollectionAPI_PutSavedItem(t *testing.T) {
	t.Run("creates the collection and moves the saved item through HTTP", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-api-create@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/articles/1",
		)

		resp := putIntoCollection(t, userID, savedItemID, "Wishlist")
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var body putCollectionResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		require.Equal(
			t,
			"saved item moved into collection successfully",
			body.Message,
		)
		require.Equal(t, "Wishlist", body.Data.Collection.Name)
		require.Equal(t, "user", body.Data.Collection.Type)
		require.NotEmpty(t, body.Data.Collection.CreatedAt)
		require.NotEmpty(t, body.Data.Collection.UpdatedAt)
		require.True(t, body.Data.CollectionCreated)
		require.False(t, body.Data.AlreadyInCollection)

		// The saved item the response reports is the one now filed there.
		require.Equal(t, savedItemID.String(), body.Data.SavedItem.ID)
		require.Equal(
			t,
			body.Data.Collection.ID,
			body.Data.SavedItem.CollectionID,
		)
		require.Equal(t, "https://example.com/articles/1", body.Data.SavedItem.URL)
		require.NotNil(t, body.Data.SavedItem.Domain)

		// And the database agrees.
		require.Equal(t, int64(1), countCollectionsNamed(t, userID, "Wishlist"))
		require.Equal(
			t,
			mustParseUUID(t, body.Data.Collection.ID),
			savedItemState(t, savedItemID).CollectionID,
		)
	})

	t.Run("reuses the collection on a second call", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-api-reuse@example.com")
		unsortedID := createUnsorted(t, userID)

		first := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/first",
		)
		second := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/second",
		)

		firstResp := putIntoCollection(t, userID, first, "Wishlist")
		require.Equal(t, http.StatusOK, firstResp.StatusCode)

		var firstBody putCollectionResponse
		require.NoError(t, json.NewDecoder(firstResp.Body).Decode(&firstBody))
		require.True(t, firstBody.Data.CollectionCreated)

		// Differently cased and padded: same collection per the unique index.
		secondResp := putIntoCollection(t, userID, second, "  wishLIST ")
		require.Equal(t, http.StatusOK, secondResp.StatusCode)

		var secondBody putCollectionResponse
		require.NoError(t, json.NewDecoder(secondResp.Body).Decode(&secondBody))

		require.False(t, secondBody.Data.CollectionCreated)
		require.Equal(
			t,
			firstBody.Data.Collection.ID,
			secondBody.Data.Collection.ID,
		)
		require.Equal(t, int64(1), countCollectionsNamed(t, userID, "Wishlist"))
	})

	t.Run("is idempotent over HTTP and leaves updated_at alone", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-api-idempotent@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/idempotent",
		)

		require.Equal(
			t,
			http.StatusOK,
			putIntoCollection(t, userID, savedItemID, "Wishlist").StatusCode,
		)

		afterFirst := savedItemState(t, savedItemID)

		resp := putIntoCollection(t, userID, savedItemID, "Wishlist")
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var body putCollectionResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		require.True(t, body.Data.AlreadyInCollection)
		require.False(t, body.Data.CollectionCreated)

		// A repeat call must not look like a move.
		require.Equal(
			t,
			afterFirst.UpdatedAt,
			savedItemState(t, savedItemID).UpdatedAt,
		)
	})

	t.Run("returns 409 for a system collection name", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-api-reserved@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/reserved",
		)

		resp := putIntoCollection(t, userID, savedItemID, "Unsorted")
		require.Equal(t, http.StatusConflict, resp.StatusCode)

		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		require.Equal(
			t,
			collection.CodeCollectionNameReserved,
			body.Error.Code,
		)

		// No user collection was created and the item stayed in Unsorted.
		require.Equal(t, int64(1), countCollectionsNamed(t, userID, "Unsorted"))
		require.Equal(t, unsortedID, savedItemState(t, savedItemID).CollectionID)
	})

	t.Run("returns 404 for a foreign saved item and creates no collection", func(t *testing.T) {
		truncateCollectionData(t)

		ownerID := createCollectionUser(t, "collection-api-owner@example.com")
		otherUserID := createCollectionUser(t, "collection-api-other@example.com")

		ownerUnsortedID := createUnsorted(t, ownerID)
		savedItemID := createSavedItemInCollection(
			t,
			ownerID,
			ownerUnsortedID,
			"https://example.com/someone-elses",
		)

		resp := putIntoCollection(t, otherUserID, savedItemID, "Wishlist")
		require.Equal(t, http.StatusNotFound, resp.StatusCode)

		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, collection.CodeSavedItemNotFound, body.Error.Code)

		require.Equal(t, int64(0), countCollectionsNamed(t, otherUserID, "Wishlist"))
		require.Equal(t, int64(0), countCollectionsNamed(t, ownerID, "Wishlist"))
		require.Equal(t, ownerUnsortedID, savedItemState(t, savedItemID).CollectionID)
	})

	t.Run("404 bodies are identical for unknown and foreign ids", func(t *testing.T) {
		truncateCollectionData(t)

		ownerID := createCollectionUser(t, "collection-api-equal-owner@example.com")
		otherUserID := createCollectionUser(t, "collection-api-equal-other@example.com")

		ownerUnsortedID := createUnsorted(t, ownerID)
		ownedItemID := createSavedItemInCollection(
			t,
			ownerID,
			ownerUnsortedID,
			"https://example.com/owned",
		)

		bodyFor := func(savedItemID uuid.UUID) string {
			resp := putIntoCollection(t, otherUserID, savedItemID, "Wishlist")
			require.Equal(t, http.StatusNotFound, resp.StatusCode)

			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			return string(raw)
		}

		require.Equal(t, bodyFor(uuid.New()), bodyFor(ownedItemID))

		require.Equal(t, int64(0), countCollectionsNamed(t, otherUserID, "Wishlist"))
	})

	t.Run("returns 400 for a blank collection name", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-api-blank@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/blank",
		)

		resp := putIntoCollection(t, userID, savedItemID, "   ")
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(
			t,
			collection.CodeInvalidCollectionName,
			body.Error.Code,
		)

		require.Equal(t, unsortedID, savedItemState(t, savedItemID).CollectionID)
	})

	t.Run("returns 400 for an invalid saved item id", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodPut,
			"/saved-items/not-a-uuid/collection",
			strings.NewReader(`{"collection_name": "Wishlist"}`),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(
			"Authorization",
			"Bearer "+newTestAccessToken(t, createCollectionUser(
				t,
				"collection-api-invalid-id@example.com",
			)),
		)

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "BAD_REQUEST", body.Error.Code)
	})

	t.Run("returns 401 without an access token", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodPut,
			"/saved-items/"+uuid.New().String()+"/collection",
			strings.NewReader(`{"collection_name": "Wishlist"}`),
		)
		req.Header.Set("Content-Type", "application/json")

		resp, err := testApp.Test(req)
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

	t.Run("does not collide with the saved item routes", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-api-routes@example.com")
		unsortedID := createUnsorted(t, userID)
		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/routes",
		)

		accessToken := newTestAccessToken(t, userID)
		ctx := context.Background()

		// The collection module mounts under the same /saved-items prefix. The
		// existing saved item routes must keep working unchanged, and the listing is
		// addressed through its collection rather than through the shared prefix.
		getReq := httptest.NewRequest(
			http.MethodGet,
			"/saved-items/"+savedItemID.String(),
			nil,
		)
		getReq.Header.Set("Authorization", "Bearer "+accessToken)

		getResp, err := testApp.Test(getReq)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, getResp.StatusCode)

		listReq := httptest.NewRequest(
			http.MethodGet,
			"/collections/"+unsortedID.String()+"/saved-items",
			nil,
		)
		listReq.Header.Set("Authorization", "Bearer "+accessToken)

		listResp, err := testApp.Test(listReq)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, listResp.StatusCode)

		deleteReq := httptest.NewRequest(
			http.MethodDelete,
			"/saved-items/"+savedItemID.String(),
			nil,
		)
		deleteReq.Header.Set("Authorization", "Bearer "+accessToken)

		deleteResp, err := testApp.Test(deleteReq)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, deleteResp.StatusCode)

		remaining, err := saveditemdbtest.New(testPool).CountSavedItemsForUser(
			ctx,
			userID,
		)
		require.NoError(t, err)
		require.Equal(t, int64(0), remaining)
	})
}
