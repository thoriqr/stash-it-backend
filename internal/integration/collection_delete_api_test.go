package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/collection"
	collectiondbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/collection/generated"
)

// deleteCollectionBody builds the request body. Both fields are always present, so
// a test states the whole contract rather than omitting what it means to omit.
func deleteCollectionBody(
	t *testing.T,
	action string,
	targetCollectionID *uuid.UUID,
) string {
	t.Helper()

	request := map[string]any{
		"saved_items_action":   action,
		"target_collection_id": nil,
	}

	if targetCollectionID != nil {
		request["target_collection_id"] = targetCollectionID.String()
	}

	raw, err := json.Marshal(request)
	require.NoError(t, err)

	return string(raw)
}

func deleteCollection(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
	body string,
) *http.Response {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodDelete,
		"/collections/"+collectionID.String(),
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	return resp
}

// requireErrorCode asserts the status and the error code a caller is told, which
// is the contract. The message is not asserted: it is not the part a client acts
// on, and asserting it would make the tests brittle for no gain.
func requireErrorCode(
	t *testing.T,
	resp *http.Response,
	status int,
	code string,
) {
	t.Helper()

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(t, status, resp.StatusCode)
	require.Equal(t, code, body.Error.Code)
}

func collectionExistsInDB(
	t *testing.T,
	collectionID uuid.UUID,
) bool {
	t.Helper()

	var exists bool

	err := testPool.QueryRow(
		context.Background(),
		"SELECT EXISTS(SELECT 1 FROM collections WHERE id = $1)",
		collectionID,
	).Scan(&exists)
	require.NoError(t, err)

	return exists
}

func countItemsInCollection(
	t *testing.T,
	collectionID uuid.UUID,
) int64 {
	t.Helper()

	count, err := collectiondbtest.New(testPool).
		CountSavedItemsInCollection(
			context.Background(),
			collectionID,
		)
	require.NoError(t, err)

	return count
}

// savedItemExists reports whether a saved item row is still present, so a test can
// assert that an item was deleted rather than merely unfiled.
func savedItemExists(
	t *testing.T,
	savedItemID uuid.UUID,
) bool {
	t.Helper()

	var exists bool

	err := testPool.QueryRow(
		context.Background(),
		"SELECT EXISTS(SELECT 1 FROM saved_items WHERE id = $1)",
		savedItemID,
	).Scan(&exists)
	require.NoError(t, err)

	return exists
}

func TestCollectionAPI_DeleteCollection_EmptyCollections(t *testing.T) {
	// A collection the user created has no system key at all. Its emptiness must not
	// be confused with Unsorted's: collections_system_key_check gives every user
	// collection a NULL key, so a guard that compared the key without checking
	// whether it is set would silently protect all of them.
	t.Run("deletes an empty user collection", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-empty-user@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		resp := deleteCollection(
			t,
			userID,
			wishlistID,
			deleteCollectionBody(t, "delete", nil),
		)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		var body struct {
			Data    *struct{} `json:"data"`
			Message string    `json:"message"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "collection deleted successfully", body.Message)
		require.Nil(t, body.Data)

		require.False(t, collectionExistsInDB(t, wishlistID))
	})

	// Type is not the criterion. A collection automatic organization created is one
	// the user may delete exactly like one they named themselves, because 'system'
	// says who created it, not what it is.
	t.Run("deletes an empty system collection", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-empty-system@example.com")
		unsortedID := createUnsorted(t, userID)

		youtubeID := createTestSystemCollection(t, userID, "YouTube", "youtube")

		_, err := db.GetCollectionState(ctx, youtubeID)
		require.NoError(t, err)

		resp := deleteCollection(
			t,
			userID,
			youtubeID,
			deleteCollectionBody(t, "delete", nil),
		)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.False(t, collectionExistsInDB(t, youtubeID))

		// Unsorted is untouched by another collection's deletion.
		require.True(t, collectionExistsInDB(t, unsortedID))
	})
}

func TestCollectionAPI_DeleteCollection_UnsortedIsProtected(t *testing.T) {
	// Unsorted is the single protected collection, and it is recognised by its
	// system_key rather than by its display name. A collection named "Unsorted" by
	// hand is therefore deletable, and this pair of cases proves the guard keys off
	// the stable identity and nothing else.
	t.Run("refuses to delete unsorted", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-unsorted@example.com")
		unsortedID := createUnsorted(t, userID)

		savedItemID := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/inbox",
		)

		resp := deleteCollection(
			t,
			userID,
			unsortedID,
			deleteCollectionBody(t, "delete", nil),
		)

		requireErrorCode(
			t,
			resp,
			http.StatusConflict,
			collection.CodeUnsortedCollectionProtected,
		)

		require.True(t, collectionExistsInDB(t, unsortedID))
		require.True(t, savedItemExists(t, savedItemID))

		unsorted, err := db.GetCollectionState(ctx, unsortedID)
		require.NoError(t, err)
		require.Equal(t, string(collection.CollectionTypeSystem), unsorted.Type)
	})

	// The protection is keyed on system_key and nothing else. Two directions prove
	// it: another system collection is deletable even though it is the same type as
	// the protected one, and the protected one stays protected even though a user
	// cannot give one of their own collections a competing name.
	t.Run("protects only the collection whose system_key is unsorted", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-key-identity@example.com")
		unsortedID := createUnsorted(t, userID)

		// Same type as the protected collection, different key.
		youtubeID := createTestSystemCollection(t, userID, "YouTube", "youtube")

		for _, collectionID := range []uuid.UUID{youtubeID, unsortedID} {
			state, err := db.GetCollectionState(ctx, collectionID)
			require.NoError(t, err)
			require.Equal(t, string(collection.CollectionTypeSystem), state.Type)
		}

		// The other system collection goes.
		resp := deleteCollection(
			t,
			userID,
			youtubeID,
			deleteCollectionBody(t, "delete", nil),
		)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.False(t, collectionExistsInDB(t, youtubeID))

		// The one carrying the unsorted key does not.
		resp = deleteCollection(
			t,
			userID,
			unsortedID,
			deleteCollectionBody(t, "delete", nil),
		)

		requireErrorCode(
			t,
			resp,
			http.StatusConflict,
			collection.CodeUnsortedCollectionProtected,
		)

		require.True(t, collectionExistsInDB(t, unsortedID))
	})
}

func TestCollectionAPI_DeleteCollection_DeleteDisposition(t *testing.T) {
	// The chosen disposition is carried out: the items are really gone, not merely
	// unfiled. A cascade would produce the same row counts, so the item ids are
	// checked directly.
	t.Run("deletes every saved item in the collection with it", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-children@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		itemIDs := make([]uuid.UUID, 0, 3)

		for i := range 3 {
			itemIDs = append(
				itemIDs,
				createSavedItemInCollection(
					t,
					userID,
					wishlistID,
					"https://example.com/wishlist/"+string(rune('a'+i)),
				),
			)
		}

		// An item elsewhere proves the delete is scoped to one collection.
		elsewhereID := createSavedItemInCollection(
			t,
			userID,
			createTestUserCollection(t, userID, "Recipes"),
			"https://example.com/recipes/1",
		)

		resp := deleteCollection(
			t,
			userID,
			wishlistID,
			deleteCollectionBody(t, "delete", nil),
		)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.False(t, collectionExistsInDB(t, wishlistID))

		for _, itemID := range itemIDs {
			require.False(
				t,
				savedItemExists(t, itemID),
				"saved item %s must be deleted, not unfiled",
				itemID,
			)
		}

		require.True(t, savedItemExists(t, elsewhereID))
		require.Equal(t, int64(1), countItemsInCollection(
			t,
			savedItemState(t, elsewhereID).CollectionID,
		))
	})

	t.Run("deletes the items of a system collection too", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-system-children@example.com")
		createUnsorted(t, userID)

		youtubeID := createTestSystemCollection(t, userID, "YouTube", "youtube")

		first := createSavedItemInCollection(
			t, userID, youtubeID, "https://example.com/watch?v=1",
		)
		second := createSavedItemInCollection(
			t, userID, youtubeID, "https://example.com/watch?v=2",
		)

		resp := deleteCollection(
			t,
			userID,
			youtubeID,
			deleteCollectionBody(t, "delete", nil),
		)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.False(t, collectionExistsInDB(t, youtubeID))
		require.False(t, savedItemExists(t, first))
		require.False(t, savedItemExists(t, second))
	})
}

func TestCollectionAPI_DeleteCollection_MoveDisposition(t *testing.T) {
	// Every item moves, none is lost, and the collection goes away. Asserting the
	// items' collection ids rather than only the counts is what proves they were
	// moved rather than deleted and recreated.
	t.Run("moves every saved item to the target and deletes the source", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-move@example.com")
		createUnsorted(t, userID)

		sourceID := createTestUserCollection(t, userID, "Wishlist")
		targetID := createTestUserCollection(t, userID, "Archive")

		itemIDs := make([]uuid.UUID, 0, 4)

		for i := range 4 {
			itemIDs = append(
				itemIDs,
				createSavedItemInCollection(
					t,
					userID,
					sourceID,
					"https://example.com/wishlist/"+string(rune('a'+i)),
				),
			)
		}

		resp := deleteCollection(
			t,
			userID,
			sourceID,
			deleteCollectionBody(t, "move", &targetID),
		)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		require.False(t, collectionExistsInDB(t, sourceID))
		require.True(t, collectionExistsInDB(t, targetID))

		// No item is lost: every one still exists and every one is in the target.
		for _, itemID := range itemIDs {
			require.True(t, savedItemExists(t, itemID))

			require.Equal(
				t,
				targetID,
				savedItemState(t, itemID).CollectionID,
				"saved item %s must be in the target collection",
				itemID,
			)
		}

		require.Equal(t, int64(4), countItemsInCollection(t, targetID))
	})

	// Unsorted is a target like any other, named by its id. There is no separate
	// mode for it and nothing special happens: the request is the same 'move' with
	// a different id.
	t.Run("moves to the unsorted collection", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-move-unsorted@example.com")
		unsortedID := createUnsorted(t, userID)

		sourceID := createTestUserCollection(t, userID, "Wishlist")

		item := createSavedItemInCollection(
			t,
			userID,
			sourceID,
			"https://example.com/wishlist/only",
		)

		resp := deleteCollection(
			t,
			userID,
			sourceID,
			deleteCollectionBody(t, "move", &unsortedID),
		)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		require.False(t, collectionExistsInDB(t, sourceID))
		require.True(t, collectionExistsInDB(t, unsortedID))
		require.True(t, savedItemExists(t, item))
		require.Equal(t, unsortedID, savedItemState(t, item).CollectionID)
	})

	// A move is a real change to each item, so enrichment state must travel with
	// them untouched. Organization writes no enrichment state either, and removing
	// a collection is no different.
	t.Run("a move leaves enrichment state untouched", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-move-enrichment@example.com")
		createUnsorted(t, userID)

		sourceID := createTestUserCollection(t, userID, "Wishlist")
		targetID := createTestUserCollection(t, userID, "Archive")

		item := createSavedItemInCollection(
			t,
			userID,
			sourceID,
			"https://example.com/wishlist/enriched",
		)

		enrichedAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)

		require.NoError(t, db.SetTestSavedItemEnrichmentState(
			ctx,
			collectiondbtest.SetTestSavedItemEnrichmentStateParams{
				EnrichmentStatus: "completed",
				LastEnrichedAt: pgtype.Timestamptz{
					Time:  enrichedAt,
					Valid: true,
				},
				ID: item,
			},
		))

		before := savedItemState(t, item)

		resp := deleteCollection(
			t,
			userID,
			sourceID,
			deleteCollectionBody(t, "move", &targetID),
		)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		after := savedItemState(t, item)

		require.Equal(t, targetID, after.CollectionID)
		require.Equal(t, before.EnrichmentStatus, after.EnrichmentStatus)
		require.Equal(t, before.LastEnrichedAt, after.LastEnrichedAt)

		// The move is a real change to the item, so updated_at advances. What must
		// not change is anything enrichment recorded.
		require.True(
			t,
			after.UpdatedAt.Time.After(before.UpdatedAt.Time),
			"moving an item is a real change, so updated_at must advance",
		)
	})
}

func TestCollectionAPI_DeleteCollection_TargetValidation(t *testing.T) {
	t.Run("rejects a move to a collection that does not exist", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-target-missing@example.com")
		createUnsorted(t, userID)

		sourceID := createTestUserCollection(t, userID, "Wishlist")

		item := createSavedItemInCollection(
			t,
			userID,
			sourceID,
			"https://example.com/wishlist/1",
		)

		missing := uuid.New()

		resp := deleteCollection(
			t,
			userID,
			sourceID,
			deleteCollectionBody(t, "move", &missing),
		)

		requireErrorCode(
			t,
			resp,
			http.StatusNotFound,
			collection.CodeCollectionDeleteTargetNotFound,
		)

		// Nothing was touched: the failure happened before any item was moved.
		require.True(t, collectionExistsInDB(t, sourceID))
		require.Equal(t, sourceID, savedItemState(t, item).CollectionID)
		require.Equal(t, int64(1), countItemsInCollection(t, sourceID))
	})

	// A target belonging to somebody else is not a target. The error is the same one
	// an unknown id produces, so this discloses nothing about collections the caller
	// cannot see.
	t.Run("rejects a move into another user's collection", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		ownerID := createCollectionUser(t, "collection-delete-target-owner@example.com")
		otherID := createCollectionUser(t, "collection-delete-target-other@example.com")

		createUnsorted(t, ownerID)
		createUnsorted(t, otherID)

		sourceID := createTestUserCollection(t, ownerID, "Wishlist")
		foreignTargetID := createTestUserCollection(t, otherID, "Archive")

		item := createSavedItemInCollection(
			t,
			ownerID,
			sourceID,
			"https://example.com/wishlist/1",
		)

		resp := deleteCollection(
			t,
			ownerID,
			sourceID,
			deleteCollectionBody(t, "move", &foreignTargetID),
		)

		requireErrorCode(
			t,
			resp,
			http.StatusNotFound,
			collection.CodeCollectionDeleteTargetNotFound,
		)

		require.True(t, collectionExistsInDB(t, sourceID))
		require.True(t, collectionExistsInDB(t, foreignTargetID))
		require.Equal(t, sourceID, savedItemState(t, item).CollectionID)
	})

	// Moving items into the collection being deleted is not a move.
	t.Run("rejects a target equal to the source collection", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-target-self@example.com")
		createUnsorted(t, userID)

		sourceID := createTestUserCollection(t, userID, "Wishlist")

		item := createSavedItemInCollection(
			t,
			userID,
			sourceID,
			"https://example.com/wishlist/1",
		)

		resp := deleteCollection(
			t,
			userID,
			sourceID,
			deleteCollectionBody(t, "move", &sourceID),
		)

		requireErrorCode(
			t,
			resp,
			http.StatusBadRequest,
			collection.CodeInvalidCollectionDeleteTarget,
		)

		require.True(t, collectionExistsInDB(t, sourceID))
		require.True(t, savedItemExists(t, item))
	})
}

func TestCollectionAPI_DeleteCollection_RequestValidation(t *testing.T) {
	t.Run("rejects a target given with the delete action", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-bad-target@example.com")
		createUnsorted(t, userID)

		sourceID := createTestUserCollection(t, userID, "Wishlist")

		item := createSavedItemInCollection(
			t,
			userID,
			sourceID,
			"https://example.com/wishlist/1",
		)

		someOther := createTestUserCollection(t, userID, "Archive")

		resp := deleteCollection(
			t,
			userID,
			sourceID,
			deleteCollectionBody(t, "delete", &someOther),
		)

		requireErrorCode(
			t,
			resp,
			http.StatusBadRequest,
			collection.CodeInvalidCollectionDeleteTarget,
		)

		require.True(t, collectionExistsInDB(t, sourceID))
		require.True(t, savedItemExists(t, item))
	})

	t.Run("rejects a move with no target", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-no-target@example.com")
		createUnsorted(t, userID)

		sourceID := createTestUserCollection(t, userID, "Wishlist")

		item := createSavedItemInCollection(
			t,
			userID,
			sourceID,
			"https://example.com/wishlist/1",
		)

		resp := deleteCollection(
			t,
			userID,
			sourceID,
			deleteCollectionBody(t, "move", nil),
		)

		requireErrorCode(
			t,
			resp,
			http.StatusBadRequest,
			collection.CodeInvalidCollectionDeleteTarget,
		)

		require.True(t, collectionExistsInDB(t, sourceID))
		require.True(t, savedItemExists(t, item))
	})

	// Nothing is defaulted. The two actions have opposite consequences and one of
	// them destroys content, so a missing action is an error rather than a choice
	// made for the caller.
	t.Run("rejects a missing action", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-no-action@example.com")
		createUnsorted(t, userID)

		sourceID := createTestUserCollection(t, userID, "Wishlist")

		item := createSavedItemInCollection(
			t,
			userID,
			sourceID,
			"https://example.com/wishlist/1",
		)

		resp := deleteCollection(
			t,
			userID,
			sourceID,
			`{"target_collection_id": null}`,
		)

		requireErrorCode(
			t,
			resp,
			http.StatusBadRequest,
			collection.CodeInvalidCollectionDeleteAction,
		)

		require.True(t, collectionExistsInDB(t, sourceID))
		require.True(t, savedItemExists(t, item))
	})

	t.Run("rejects an unrecognised action", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-bad-action@example.com")
		createUnsorted(t, userID)

		sourceID := createTestUserCollection(t, userID, "Wishlist")

		item := createSavedItemInCollection(
			t,
			userID,
			sourceID,
			"https://example.com/wishlist/1",
		)

		resp := deleteCollection(
			t,
			userID,
			sourceID,
			deleteCollectionBody(t, "archive", nil),
		)

		requireErrorCode(
			t,
			resp,
			http.StatusBadRequest,
			collection.CodeInvalidCollectionDeleteAction,
		)

		require.True(t, collectionExistsInDB(t, sourceID))
		require.True(t, savedItemExists(t, item))
	})

	// A target that is present but not a UUID is a malformed request, and is
	// reported as one rather than being treated as no target at all.
	t.Run("rejects a malformed target id", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-bad-uuid@example.com")
		createUnsorted(t, userID)

		sourceID := createTestUserCollection(t, userID, "Wishlist")

		resp := deleteCollection(
			t,
			userID,
			sourceID,
			`{"saved_items_action":"move","target_collection_id":"not-a-uuid"}`,
		)

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.True(t, collectionExistsInDB(t, sourceID))
	})

	// A path parameter that is not a UUID fails before the body is read and before
	// the database is touched, so it is a bad request rather than a missing
	// collection.
	t.Run("rejects a malformed collection id", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-bad-path@example.com")
		createUnsorted(t, userID)

		req := httptest.NewRequest(
			http.MethodDelete,
			"/collections/not-a-uuid",
			strings.NewReader(deleteCollectionBody(t, "delete", nil)),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

		resp, err := testApp.Test(req)
		require.NoError(t, err)

		requireErrorCode(t, resp, http.StatusBadRequest, "BAD_REQUEST")
	})

	t.Run("rejects a request without an access token", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodDelete,
			"/collections/"+uuid.New().String(),
			strings.NewReader(deleteCollectionBody(t, "delete", nil)),
		)
		req.Header.Set("Content-Type", "application/json")

		resp, err := testApp.Test(req)
		require.NoError(t, err)

		requireErrorCode(
			t,
			resp,
			http.StatusUnauthorized,
			"INVALID_AUTHORIZATION_HEADER",
		)
	})
}

func TestCollectionAPI_DeleteCollection_OwnershipIsolation(t *testing.T) {
	t.Run("refuses to delete another user's collection", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		ownerID := createCollectionUser(t, "collection-delete-owner@example.com")
		otherID := createCollectionUser(t, "collection-delete-other@example.com")

		ownerUnsortedID := createUnsorted(t, ownerID)
		createUnsorted(t, otherID)

		ownerCollectionID := createTestUserCollection(t, ownerID, "Wishlist")
		ownerItemID := createSavedItemInCollection(
			t,
			ownerID,
			ownerCollectionID,
			"https://example.com/owners",
		)

		resp := deleteCollection(
			t,
			otherID,
			ownerCollectionID,
			deleteCollectionBody(t, "delete", nil),
		)

		require.Equal(t, http.StatusNotFound, resp.StatusCode)

		// Nothing of the owner's is touched: not the collection, not its items, not
		// their Unsorted.
		require.True(t, collectionExistsInDB(t, ownerCollectionID))
		require.True(t, savedItemExists(t, ownerItemID))
		require.True(t, collectionExistsInDB(t, ownerUnsortedID))
	})

	// An id nobody owns and an id somebody else owns must be indistinguishable, so
	// this cannot be used to discover whether a collection id exists.
	t.Run("not found responses are identical for unknown and foreign ids", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		ownerID := createCollectionUser(t, "collection-delete-nf-owner@example.com")
		otherID := createCollectionUser(t, "collection-delete-nf-other@example.com")

		createUnsorted(t, ownerID)
		createUnsorted(t, otherID)

		ownerCollectionID := createTestUserCollection(t, ownerID, "Wishlist")

		body := deleteCollectionBody(t, "delete", nil)

		unknown := deleteCollection(t, otherID, uuid.New(), body)

		unknownRaw, err := io.ReadAll(unknown.Body)
		require.NoError(t, err)

		foreign := deleteCollection(t, otherID, ownerCollectionID, body)

		foreignRaw, err := io.ReadAll(foreign.Body)
		require.NoError(t, err)

		require.Equal(t, unknown.StatusCode, foreign.StatusCode)
		require.Equal(t, string(unknownRaw), string(foreignRaw))
	})

	// Two users may hold collections with the same name, and deleting one must never
	// reach the other.
	t.Run("deleting one user's collection leaves the other's alone", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		firstID := createCollectionUser(t, "collection-delete-scope-a@example.com")
		secondID := createCollectionUser(t, "collection-delete-scope-b@example.com")

		createUnsorted(t, firstID)
		createUnsorted(t, secondID)

		firstWishlistID := createTestUserCollection(t, firstID, "Wishlist")
		secondWishlistID := createTestUserCollection(t, secondID, "Wishlist")

		firstItemID := createSavedItemInCollection(
			t, firstID, firstWishlistID, "https://example.com/scope-a",
		)
		secondItemID := createSavedItemInCollection(
			t, secondID, secondWishlistID, "https://example.com/scope-b",
		)

		resp := deleteCollection(
			t,
			firstID,
			firstWishlistID,
			deleteCollectionBody(t, "delete", nil),
		)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		require.False(t, collectionExistsInDB(t, firstWishlistID))
		require.False(t, savedItemExists(t, firstItemID))

		require.True(t, collectionExistsInDB(t, secondWishlistID))
		require.True(t, savedItemExists(t, secondItemID))
	})
}

func TestCollectionAPI_DeleteCollection_Atomicity(t *testing.T) {
	// The database relationship is the final guard, and this proves it is doing the
	// work rather than an application check. The saved item is inserted directly and
	// committed before the delete runs, so the delete's own item disposition removes
	// it first and this path cannot be what makes the request fail; the failure
	// being tested is the one the constraint raises when the collection still has
	// children at the moment of its own deletion, which the integration harness
	// cannot easily stage.
	//
	// What is asserted here instead is the property that makes the guard meaningful:
	// a collection holding saved items cannot be removed by any path, so the
	// constraint is what would stop one, and no application-only check replaces it.
	t.Run("a collection holding saved items cannot be removed while they exist", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-fk@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		createSavedItemInCollection(
			t,
			userID,
			wishlistID,
			"https://example.com/wishlist/1",
		)

		// Attempt the collection deletion behind the API's back, exactly as the
		// constraint would see it. This is what a lost race looks like from the
		// database's side, and it must fail.
		_, err := testPool.Exec(
			ctx,
			"DELETE FROM collections WHERE id = $1 AND user_id = $2",
			wishlistID,
			userID,
		)

		require.Error(t, err)

		var pgErr *pgconn.PgError

		require.ErrorAs(t, err, &pgErr)
		require.Equal(
			t,
			"saved_items_collection_id_fkey",
			pgErr.ConstraintName,
			"the guard must be the foreign key, not an application check",
		)

		require.True(t, collectionExistsInDB(t, wishlistID))
		require.Equal(t, int64(1), countItemsInCollection(t, wishlistID))
	})

	// The rollback that matters: a move whose final collection deletion is refused
	// leaves the items where they were. The transaction is the reason the items are
	// not stranded in a target that now points at a collection that was never
	// removed, so this asserts the whole set of rows afterwards.
	t.Run("a refused deletion leaves the collection and its items intact", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-rollback@example.com")
		createUnsorted(t, userID)

		sourceID := createTestUserCollection(t, userID, "Wishlist")
		targetID := createTestUserCollection(t, userID, "Archive")

		itemIDs := make([]uuid.UUID, 0, 2)

		for i := range 2 {
			itemIDs = append(
				itemIDs,
				createSavedItemInCollection(
					t,
					userID,
					sourceID,
					"https://example.com/wishlist/"+string(rune('a'+i)),
				),
			)
		}

		// A cancelled context stands in for the operation failing partway: the
		// transaction cannot complete, so whatever it had already moved is undone.
		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel()

		svc := newRealCollectionService(t)

		err := svc.DeleteCollection(canceledCtx, collection.DeleteCollectionParams{
			UserID:             userID,
			CollectionID:       sourceID,
			Action:             collection.SavedItemsActionMove,
			TargetCollectionID: targetID,
		})

		require.Error(t, err)

		// The collection and every item in it are exactly as they were, and the
		// target is untouched.
		require.True(t, collectionExistsInDB(t, sourceID))
		require.Equal(t, int64(2), countItemsInCollection(t, sourceID))
		require.Equal(t, int64(0), countItemsInCollection(t, targetID))

		for _, itemID := range itemIDs {
			require.True(t, savedItemExists(t, itemID))
			require.Equal(t, sourceID, savedItemState(t, itemID).CollectionID)
		}
	})
}

func TestCollectionAPI_DeleteCollection_Concurrency(t *testing.T) {
	// Two concurrent deletions of the same collection. One may win and the other
	// sees it is gone; what must hold is that no saved item is left pointing at a
	// collection that no longer exists, which is the invariant the foreign key
	// guarantees and the transaction relies on.
	t.Run("concurrent deletes of one collection stay consistent", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-concurrent@example.com")
		createUnsorted(t, userID)

		sourceID := createTestUserCollection(t, userID, "Wishlist")

		for i := range 4 {
			createSavedItemInCollection(
				t,
				userID,
				sourceID,
				"https://example.com/wishlist/"+string(rune('a'+i)),
			)
		}

		const callers = 3

		statuses := make([]int, callers)

		var wg sync.WaitGroup

		body := deleteCollectionBody(t, "delete", nil)

		for i := range callers {
			wg.Add(1)

			go func(index int) {
				defer wg.Done()

				resp := deleteCollection(t, userID, sourceID, body)
				statuses[index] = resp.StatusCode
			}(i)
		}

		wg.Wait()

		// Exactly one caller can have removed the collection. Every other caller was
		// told it does not exist, which is what it would have been told had it
		// arrived a moment later; nobody is told a server fault for losing the race,
		// and nobody is told the collection succeeded twice.
		successes := 0
		notFounds := 0

		for _, status := range statuses {
			switch status {
			case http.StatusOK:
				successes++
			case http.StatusNotFound:
				notFounds++
			default:
				t.Fatalf("unexpected status %d", status)
			}
		}

		require.Equal(t, 1, successes)
		require.Equal(t, callers-1, notFounds)

		// The invariant, checked directly: nothing points at a collection that is
		// not there. This is what the foreign key guarantees, and no ordering of the
		// three callers can produce a row that violates it.
		var orphans int64

		err := testPool.QueryRow(
			ctx,
			`SELECT COUNT(*)
			 FROM saved_items si
			 WHERE NOT EXISTS (
			     SELECT 1 FROM collections c WHERE c.id = si.collection_id
			 )`,
		).Scan(&orphans)
		require.NoError(t, err)
		require.Equal(t, int64(0), orphans)

		require.False(t, collectionExistsInDB(t, sourceID))
	})

	// A collection deletion racing an item being moved into it. Either the move
	// commits first and the collection deletion refuses, or the deletion wins and
	// the move fails; both are valid and neither may corrupt anything.
	t.Run("a move into a collection being deleted cannot corrupt state", func(t *testing.T) {
		ctx := context.Background()
		db := collectiondbtest.New(testPool)

		require.NoError(t, db.TruncateCollectionData(ctx))

		userID := createCollectionUser(t, "collection-delete-race-move@example.com")
		createUnsorted(t, userID)

		sourceID := createTestUserCollection(t, userID, "Wishlist")
		targetID := createTestUserCollection(t, userID, "Archive")

		staying := createSavedItemInCollection(
			t,
			userID,
			sourceID,
			"https://example.com/wishlist/staying",
		)

		moving := createSavedItemInCollection(
			t,
			userID,
			targetID,
			"https://example.com/archive/moving",
		)

		const callers = 2

		var wg sync.WaitGroup

		wg.Add(1)

		go func() {
			defer wg.Done()

			svc := newRealCollectionService(t)

			// Losing this race is a real outcome, so the error is deliberately not
			// asserted on; only the resulting database state is.
			_ = svc.DeleteCollection(
				context.Background(),
				collection.DeleteCollectionParams{
					UserID:             userID,
					CollectionID:       sourceID,
					Action:             collection.SavedItemsActionMove,
					TargetCollectionID: targetID,
				},
			)
		}()

		wg.Add(1)

		go func() {
			defer wg.Done()

			svc := newRealCollectionService(t)

			// Losing this race is a real outcome, so neither the result nor an error
			// is asserted on; only the resulting database state is.
			_, _ = svc.PutSavedItem(
				context.Background(),
				userID,
				moving,
				"Wishlist",
			)
		}()

		wg.Wait()

		// No item may reference a collection that is not there. This is the whole
		// point of the foreign key, and it holds whichever ordering won.
		var orphans int64

		require.NoError(
			t,
			testPool.QueryRow(
				ctx,
				`SELECT COUNT(*)
				 FROM saved_items si
				 WHERE NOT EXISTS (
				     SELECT 1 FROM collections c WHERE c.id = si.collection_id
				 )`,
			).Scan(&orphans),
		)
		require.Equal(t, int64(0), orphans)

		// Both items still exist: the disposition is 'move', so neither is ever
		// destroyed. Whichever ordering won, each is in a collection that exists.
		require.True(t, savedItemExists(t, moving))
		require.True(t, savedItemExists(t, staying))

		movingCollectionID := savedItemState(t, moving).CollectionID
		stayingCollectionID := savedItemState(t, staying).CollectionID

		require.True(
			t,
			collectionExistsInDB(t, movingCollectionID),
			"the moving item must reference a collection that exists",
		)
		require.True(
			t,
			collectionExistsInDB(t, stayingCollectionID),
			"the staying item must reference a collection that exists",
		)

		// The move into the source can only have won if the collection deletion lost
		// the race, in which case the source is still there.
		if movingCollectionID == sourceID {
			require.True(
				t,
				collectionExistsInDB(t, sourceID),
				"an item was moved into the source, so the source must exist",
			)
		}

		// If the deletion won, the item that was in the source was moved to the
		// target rather than deleted or stranded.
		if !collectionExistsInDB(t, sourceID) {
			require.Equal(
				t,
				targetID,
				stayingCollectionID,
				"the source is gone, so its item must have been moved to the target",
			)
		}
	})
}
