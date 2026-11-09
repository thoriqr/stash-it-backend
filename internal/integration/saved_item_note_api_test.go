package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	collectiondbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/collection/generated"
	saveditemdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/saved_item/generated"
)

// postSavedItemBody saves a URL with an arbitrary JSON body and returns the response
// and the decoded body. A note can be omitted, null, or a multiline string, so the
// body is supplied literally rather than through a typed struct that would round it
// into one shape.
func postSavedItemBody(
	t *testing.T,
	userID uuid.UUID,
	body string,
) (*http.Response, map[string]any) {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/saved-items",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	return resp, decodeObject(t, resp.Body)
}

// putSavedItemNote calls the note add/edit endpoint.
func putSavedItemNote(
	t *testing.T,
	userID uuid.UUID,
	savedItemID uuid.UUID,
	body string,
) (*http.Response, map[string]any) {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPut,
		"/saved-items/"+savedItemID.String()+"/note",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	return resp, decodeObject(t, resp.Body)
}

// deleteSavedItemNote calls the note clearing endpoint.
func deleteSavedItemNote(
	t *testing.T,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) (*http.Response, map[string]any) {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodDelete,
		"/saved-items/"+savedItemID.String()+"/note",
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	return resp, decodeObject(t, resp.Body)
}

// noteFromState reads the stored note from a saved item's row.
func noteFromState(t *testing.T, savedItemID uuid.UUID) *string {
	t.Helper()

	state := savedItemState(t, savedItemID)
	if !state.Note.Valid {
		return nil
	}

	note := state.Note.String

	return &note
}

func requireErrorResponseCode(t *testing.T, raw map[string]any, code string) {
	t.Helper()

	errObject, ok := raw["error"].(map[string]any)
	require.True(t, ok, "response has no error object")
	require.Equal(t, code, errObject["code"])
}

func TestSavedItemAPI_Create_Note(t *testing.T) {
	t.Run("create with a note returns and stores it", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-create-with@example.com")
		createUnsorted(t, userID)

		note := "Read the section about database indexing later."

		resp, raw := postSavedItemBody(
			t,
			userID,
			`{"url":"https://example.com/article","note":"`+note+`"}`,
		)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		data := dataObject(t, raw)
		item, ok := data["saved_item"].(map[string]any)
		require.True(t, ok)

		require.Equal(t, note, item["note"])

		stored := noteFromState(t, mustParseUUID(t, item["id"].(string)))
		require.NotNil(t, stored)
		require.Equal(t, note, *stored)
	})

	t.Run("create without a note stores null", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-create-without@example.com")
		createUnsorted(t, userID)

		resp, raw := postSavedItemBody(
			t,
			userID,
			`{"url":"https://example.com/article"}`,
		)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		item := dataObject(t, raw)["saved_item"].(map[string]any)

		require.Contains(t, item, "note")
		require.Nil(t, item["note"])
		require.Nil(t, noteFromState(t, mustParseUUID(t, item["id"].(string))))
	})

	t.Run("create with a null note stores null", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-create-null@example.com")
		createUnsorted(t, userID)

		resp, raw := postSavedItemBody(
			t,
			userID,
			`{"url":"https://example.com/article","note":null}`,
		)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		item := dataObject(t, raw)["saved_item"].(map[string]any)

		require.Contains(t, item, "note")
		require.Nil(t, item["note"])
		require.Nil(t, noteFromState(t, mustParseUUID(t, item["id"].(string))))
	})

	t.Run("create with a multiline note preserves it", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-create-multiline@example.com")
		createUnsorted(t, userID)

		note := "First line\nSecond line\n\nFourth line"

		body, err := json.Marshal(map[string]string{
			"url":  "https://example.com/article",
			"note": note,
		})
		require.NoError(t, err)

		resp, raw := postSavedItemBody(t, userID, string(body))
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		item := dataObject(t, raw)["saved_item"].(map[string]any)
		require.Equal(t, note, item["note"])

		stored := noteFromState(t, mustParseUUID(t, item["id"].(string)))
		require.NotNil(t, stored)
		require.Equal(t, note, *stored)
	})

	t.Run("create normalizes a whitespace-only note to null", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-create-whitespace@example.com")
		createUnsorted(t, userID)

		body, err := json.Marshal(map[string]string{
			"url":  "https://example.com/article",
			"note": " \n\t  ",
		})
		require.NoError(t, err)

		resp, raw := postSavedItemBody(t, userID, string(body))
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		item := dataObject(t, raw)["saved_item"].(map[string]any)
		require.Nil(t, item["note"])
		require.Nil(t, noteFromState(t, mustParseUUID(t, item["id"].(string))))
	})

	t.Run("create rejects a note over 5000 characters", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-create-toolong@example.com")
		createUnsorted(t, userID)

		body, err := json.Marshal(map[string]string{
			"url":  "https://example.com/article",
			"note": strings.Repeat("a", saved_item.NoteMaxLength+1),
		})
		require.NoError(t, err)

		resp, raw := postSavedItemBody(t, userID, string(body))
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		requireErrorResponseCode(t, raw, "VALIDATION_ERROR")

		ctx := context.Background()
		db := saveditemdbtest.New(testPool)
		count, err := db.CountSavedItemsForUser(ctx, userID)
		require.NoError(t, err)
		require.Zero(t, count, "a rejected note must not create a saved item")
	})

	t.Run("create accepts exactly 5000 characters", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-create-boundary@example.com")
		createUnsorted(t, userID)

		note := strings.Repeat("a", saved_item.NoteMaxLength)

		body, err := json.Marshal(map[string]string{
			"url":  "https://example.com/article",
			"note": note,
		})
		require.NoError(t, err)

		resp, raw := postSavedItemBody(t, userID, string(body))
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		item := dataObject(t, raw)["saved_item"].(map[string]any)
		require.Equal(t, note, item["note"])
	})

	t.Run("create counts characters rather than bytes", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-create-unicode@example.com")
		createUnsorted(t, userID)

		// 5000 runes but 10000 bytes. A byte-counting limit would reject this even
		// though it is exactly at the documented character bound.
		note := strings.Repeat("é", saved_item.NoteMaxLength)
		require.Equal(t, saved_item.NoteMaxLength, utf8.RuneCountInString(note))
		require.Greater(t, len(note), saved_item.NoteMaxLength)

		body, err := json.Marshal(map[string]string{
			"url":  "https://example.com/article",
			"note": note,
		})
		require.NoError(t, err)

		resp, raw := postSavedItemBody(t, userID, string(body))
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		item := dataObject(t, raw)["saved_item"].(map[string]any)
		require.Equal(t, note, item["note"])

		stored := noteFromState(t, mustParseUUID(t, item["id"].(string)))
		require.NotNil(t, stored)
		require.Equal(t, note, *stored)
	})
}

func TestSavedItemAPI_NoteMutations(t *testing.T) {
	t.Run("PUT adds a note", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-put-add@example.com")
		unsortedID := createUnsorted(t, userID)
		itemID := createSavedItemInCollection(
			t, userID, unsortedID, "https://example.com/article",
		)

		note := "My updated context for saving this URL."

		body, err := json.Marshal(map[string]string{"note": note})
		require.NoError(t, err)

		resp, raw := putSavedItemNote(t, userID, itemID, string(body))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		data := dataObject(t, raw)
		require.Equal(t, note, data["note"])

		stored := noteFromState(t, itemID)
		require.NotNil(t, stored)
		require.Equal(t, note, *stored)
	})

	t.Run("PUT updates an existing note", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-put-update@example.com")
		unsortedID := createUnsorted(t, userID)
		itemID := createSavedItemInCollection(
			t, userID, unsortedID, "https://example.com/article",
		)

		first := `{"note":"first"}`
		resp, _ := putSavedItemNote(t, userID, itemID, first)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		second := `{"note":"second"}`
		resp, raw := putSavedItemNote(t, userID, itemID, second)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "second", dataObject(t, raw)["note"])

		stored := noteFromState(t, itemID)
		require.NotNil(t, stored)
		require.Equal(t, "second", *stored)
	})

	t.Run("PUT preserves multiline text", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-put-multiline@example.com")
		unsortedID := createUnsorted(t, userID)
		itemID := createSavedItemInCollection(
			t, userID, unsortedID, "https://example.com/article",
		)

		note := "one\ntwo\nthree"

		body, err := json.Marshal(map[string]string{"note": note})
		require.NoError(t, err)

		resp, _ := putSavedItemNote(t, userID, itemID, string(body))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		stored := noteFromState(t, itemID)
		require.NotNil(t, stored)
		require.Equal(t, note, *stored)
	})

	t.Run("PUT rejects blank and whitespace-only content", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-put-blank@example.com")
		unsortedID := createUnsorted(t, userID)
		itemID := createSavedItemInCollection(
			t, userID, unsortedID, "https://example.com/article",
		)

		for _, note := range []string{"", "   ", " \n\t "} {
			body, err := json.Marshal(map[string]string{"note": note})
			require.NoError(t, err)

			resp, raw := putSavedItemNote(t, userID, itemID, string(body))
			require.Equal(t, http.StatusBadRequest, resp.StatusCode, note)
			requireErrorResponseCode(t, raw, saved_item.CodeInvalidNote)
		}

		require.Nil(t, noteFromState(t, itemID), "a rejected value must not write")
	})

	t.Run("PUT rejects a note over 5000 characters", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-put-toolong@example.com")
		unsortedID := createUnsorted(t, userID)
		itemID := createSavedItemInCollection(
			t, userID, unsortedID, "https://example.com/article",
		)

		body, err := json.Marshal(map[string]string{
			"note": strings.Repeat("a", saved_item.NoteMaxLength+1),
		})
		require.NoError(t, err)

		resp, raw := putSavedItemNote(t, userID, itemID, string(body))
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		requireErrorResponseCode(t, raw, saved_item.CodeInvalidNote)

		require.Nil(t, noteFromState(t, itemID))
	})

	t.Run("PUT and DELETE use the same not-found behavior", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-missing@example.com")
		createUnsorted(t, userID)

		missing := uuid.New()

		resp, raw := putSavedItemNote(t, userID, missing, `{"note":"x"}`)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		requireErrorResponseCode(t, raw, "RESOURCE_NOT_FOUND")

		resp, raw = deleteSavedItemNote(t, userID, missing)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		requireErrorResponseCode(t, raw, "RESOURCE_NOT_FOUND")
	})

	t.Run("PUT rejects an invalid saved item id", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-bad-id@example.com")
		createUnsorted(t, userID)

		req := httptest.NewRequest(
			http.MethodPut,
			"/saved-items/not-a-uuid/note",
			strings.NewReader(`{"note":"x"}`),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("DELETE clears the note without deleting the item", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-delete@example.com")
		unsortedID := createUnsorted(t, userID)
		itemID := createSavedItemInCollection(
			t, userID, unsortedID, "https://example.com/article",
		)

		require.NoError(
			t,
			saveditemdbtest.New(testPool).SetTestSavedItemNote(
				context.Background(),
				saveditemdbtest.SetTestSavedItemNoteParams{
					Note: testText("a note to clear"),
					ID:   itemID,
				},
			),
		)

		resp, raw := deleteSavedItemNote(t, userID, itemID)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "saved item note deleted successfully", raw["message"])

		require.Nil(t, noteFromState(t, itemID))

		// The item itself is untouched.
		ctx := context.Background()
		state, err := saveditemdbtest.New(testPool).GetSavedItemState(ctx, itemID)
		require.NoError(t, err)
		require.Equal(t, userID, state.UserID)
		require.Equal(t, "https://example.com/article", state.Url)
	})

	t.Run("DELETE on an item with no note succeeds", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-delete-idempotent@example.com")
		unsortedID := createUnsorted(t, userID)
		itemID := createSavedItemInCollection(
			t, userID, unsortedID, "https://example.com/article",
		)

		resp, _ := deleteSavedItemNote(t, userID, itemID)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Nil(t, noteFromState(t, itemID))
	})
}

func TestSavedItemAPI_NoteOwnership(t *testing.T) {
	t.Run("another user cannot add, edit or clear a note", func(t *testing.T) {
		truncateCollectionData(t)

		ownerID := createCollectionUser(t, "note-owner@example.com")
		ownerUnsorted := createUnsorted(t, ownerID)

		itemID := createSavedItemInCollection(
			t, ownerID, ownerUnsorted, "https://example.com/private",
		)

		ctx := context.Background()
		require.NoError(
			t,
			saveditemdbtest.New(testPool).SetTestSavedItemNote(
				ctx,
				saveditemdbtest.SetTestSavedItemNoteParams{
					Note: testText("owner's note"),
					ID:   itemID,
				},
			),
		)

		otherID := createCollectionUser(t, "note-intruder@example.com")
		createUnsorted(t, otherID)

		resp, raw := putSavedItemNote(
			t,
			otherID,
			itemID,
			`{"note":"intruder's note"}`,
		)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		requireErrorResponseCode(t, raw, "RESOURCE_NOT_FOUND")

		resp, raw = deleteSavedItemNote(t, otherID, itemID)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		requireErrorResponseCode(t, raw, "RESOURCE_NOT_FOUND")

		// The owner's note and item are exactly as they were.
		stored := noteFromState(t, itemID)
		require.NotNil(t, stored)
		require.Equal(t, "owner's note", *stored)

		state, err := saveditemdbtest.New(testPool).GetSavedItemState(ctx, itemID)
		require.NoError(t, err)
		require.Equal(t, ownerID, state.UserID)
	})
}

func TestSavedItemAPI_NoteInResponses(t *testing.T) {
	t.Run("inbox listing includes the note", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-inbox@example.com")
		unsortedID := createUnsorted(t, userID)
		itemID := createSavedItemInCollection(
			t, userID, unsortedID, "https://example.com/article",
		)

		ctx := context.Background()
		require.NoError(
			t,
			saveditemdbtest.New(testPool).SetTestSavedItemNote(
				ctx,
				saveditemdbtest.SetTestSavedItemNoteParams{
					Note: testText("inbox note"),
					ID:   itemID,
				},
			),
		)

		page := listRecentSavedItemsOverHTTP(t, userID, "")
		require.Len(t, page.Data.SavedItems, 1)

		raw := listRecentSavedItemsRaw(t, userID, "")
		items := raw["data"].(map[string]any)["saved_items"].([]any)
		require.Equal(t, "inbox note", items[0].(map[string]any)["note"])
	})

	t.Run("detail includes the note and null is explicit", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-detail@example.com")
		unsortedID := createUnsorted(t, userID)

		withNote := createSavedItemInCollection(
			t, userID, unsortedID, "https://example.com/with-note",
		)
		withoutNote := createSavedItemInCollection(
			t, userID, unsortedID, "https://example.com/without-note",
		)

		require.NoError(
			t,
			saveditemdbtest.New(testPool).SetTestSavedItemNote(
				context.Background(),
				saveditemdbtest.SetTestSavedItemNoteParams{
					Note: testText("detail note"),
					ID:   withNote,
				},
			),
		)

		_, raw := getSavedItem(t, userID, withNote)
		item := dataObject(t, raw)["saved_item"].(map[string]any)
		require.Equal(t, "detail note", item["note"])

		_, raw = getSavedItem(t, userID, withoutNote)
		item = dataObject(t, raw)["saved_item"].(map[string]any)
		require.Contains(t, item, "note")
		require.Nil(t, item["note"])
	})

	t.Run("collection saved-item listing includes the note", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "note-collection-list@example.com")
		unsortedID := createUnsorted(t, userID)
		itemID := createSavedItemInCollection(
			t, userID, unsortedID, "https://example.com/article",
		)

		require.NoError(
			t,
			collectiondbtest.New(testPool).SetTestSavedItemNote(
				context.Background(),
				collectiondbtest.SetTestSavedItemNoteParams{
					Note: testText("collection note"),
					ID:   itemID,
				},
			),
		)

		page := listCollectionSavedItemsOverHTTP(t, userID, unsortedID, "")
		require.Len(t, page.Data.SavedItems, 1)

		raw := listCollectionSavedItemsRaw(t, userID, unsortedID)
		items := raw["data"].(map[string]any)["saved_items"].([]any)
		require.Equal(t, "collection note", items[0].(map[string]any)["note"])
	})
}

// listRecentSavedItemsRaw calls the inbox listing and returns the undecoded body, so a
// test can assert that a null note is present as an explicit JSON null.
func listRecentSavedItemsRaw(
	t *testing.T,
	userID uuid.UUID,
	query string,
) map[string]any {
	t.Helper()

	path := "/saved-items"
	if query != "" {
		path += "?" + query
	}

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	return decodeObject(t, resp.Body)
}

// listCollectionSavedItemsRaw is the collection listing's undecoded equivalent.
func listCollectionSavedItemsRaw(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
) map[string]any {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodGet,
		"/collections/"+collectionID.String()+"/saved-items",
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	return decodeObject(t, resp.Body)
}

func TestSavedItemAPI_NoteIsNotSearchable(t *testing.T) {
	truncateSearchData(t)

	userID, unsortedID := createSearchUserWithUnsorted(t, "note-not-searchable@example.com")

	itemID := createSearchSavedItem(
		t,
		userID,
		unsortedID,
		"https://example.org/article",
		"example.org",
		"A Camera Guide",
	)

	setSearchSavedItemNote(t, itemID, "zzzuniquenotetextzzz")

	// The note is reported on the result.
	savedItem, _ := searchResults(t, userID, "camera", itemID)
	require.Equal(t, "zzzuniquenotetextzzz", savedItem["note"])

	// But it is not part of the matching rules, so searching its text finds nothing.
	resp := runSearch(t, userID, "zzzuniquenotetextzzz", true)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Data struct {
			SavedItems []struct {
				ID uuid.UUID `json:"id"`
			} `json:"saved_items"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Empty(t, body.Data.SavedItems)
}

func TestSavedItemAPI_NoteIsIndependentOfEnrichmentAndCollections(t *testing.T) {
	truncateCollectionData(t)

	userID := createCollectionUser(t, "note-independent@example.com")
	unsortedID := createUnsorted(t, userID)
	wishlistID := createTestUserCollection(t, userID, "Wishlist")

	itemID := createSavedItemInCollection(
		t, userID, unsortedID, "https://example.com/article",
	)

	ctx := context.Background()
	require.NoError(
		t,
		saveditemdbtest.New(testPool).SetTestSavedItemNote(
			ctx,
			saveditemdbtest.SetTestSavedItemNoteParams{
				Note: testText("independent note"),
				ID:   itemID,
			},
		),
	)

	// Synchronous enrichment runs against the fake enricher the shared test app uses.
	req := httptest.NewRequest(
		http.MethodPost,
		"/saved-items/"+itemID.String()+"/enrich",
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	stored := noteFromState(t, itemID)
	require.NotNil(t, stored, "enrichment must not clear the note")
	require.Equal(t, "independent note", *stored)

	// Moving the item between collections preserves the note too.
	moveSavedItemIntoCollection(t, userID, itemID, "Wishlist")

	state := savedItemState(t, itemID)
	require.Equal(t, wishlistID, state.CollectionID)
	require.True(t, state.Note.Valid)
	require.Equal(t, "independent note", state.Note.String)

	// Enrichment ran, which is what makes the preservation above meaningful rather
	// than a statement about an operation that never happened.
	require.True(t, state.LastEnrichedAt.Valid)
}
