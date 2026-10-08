package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	saveditemdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/saved_item/generated"
)

func newTestAccessToken(t *testing.T, userID uuid.UUID) string {
	t.Helper()

	generator := security.NewAccessTokenGenerator(
		[]byte(testutil.TestAccessTokenSecret),
	)

	accessToken, err := generator.Generate(
		userID,
		uuid.New(),
		time.Hour,
	)
	require.NoError(t, err)

	return accessToken
}

func testText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}

func countSavedItemsForUser(
	t *testing.T,
	ctx context.Context,
	db *saveditemdbtest.Queries,
	userID uuid.UUID,
) int64 {
	t.Helper()

	count, err := db.CountSavedItemsForUser(ctx, userID)
	require.NoError(t, err)

	return count
}

func mustParseUUID(t *testing.T, value string) uuid.UUID {
	t.Helper()

	parsed, err := uuid.Parse(value)
	require.NoError(t, err)

	return parsed
}

// createUnsortedCollection creates the Unsorted system collection that every
// user owns and returns its id. saved_items.collection_id is NOT NULL, and the
// collection must belong to the same user as the saved item it is attached to.
func createUnsortedCollection(
	t *testing.T,
	ctx context.Context,
	db *saveditemdbtest.Queries,
	userID uuid.UUID,
) uuid.UUID {
	t.Helper()

	collectionID, err := db.CreateUnsortedCollection(ctx, userID)
	require.NoError(t, err)

	return collectionID
}

func TestSavedItem_Create(t *testing.T) {
	t.Run("saves a url for the authenticated user", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-create@example.com",
				DisplayName: "Saved Item Create User",
			},
		)
		require.NoError(t, err)

		// Migration 000022 seeds one Unsorted collection per user, and that
		// is where a newly saved item lands.
		createUnsortedCollection(t, ctx, db, userID)

		accessToken := newTestAccessToken(t, userID)

		req := httptest.NewRequest(
			http.MethodPost,
			"/saved-items",
			strings.NewReader(`{
				"url": "https://example.com/articles/1"
			}`),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var body struct {
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
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		require.Equal(t, "saved item created successfully", body.Message)
		require.NotEmpty(t, body.Data.SavedItem.ID)
		require.Equal(t, "https://example.com/articles/1", body.Data.SavedItem.URL)
		require.NotNil(t, body.Data.SavedItem.Domain)
		require.Equal(t, "example.com", *body.Data.SavedItem.Domain)
		require.Nil(t, body.Data.SavedItem.Platform)
		require.Nil(t, body.Data.SavedItem.Title)

		state, err := db.GetSavedItemState(
			ctx,
			mustParseUUID(t, body.Data.SavedItem.ID),
		)
		require.NoError(t, err)

		require.Equal(t, userID, state.UserID)
		require.Equal(t, "https://example.com/articles/1", state.Url)
		require.Equal(t, "example.com", state.Domain.String)
		require.True(t, state.Domain.Valid)
		require.False(t, state.Platform.Valid)
		require.False(t, state.Title.Valid)
		require.False(t, state.CreatedAt.Time.IsZero())
	})

	t.Run("allows duplicate urls", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-duplicate@example.com",
				DisplayName: "Saved Item Duplicate User",
			},
		)
		require.NoError(t, err)

		// Migration 000022 seeds one Unsorted collection per user, and that
		// is where a newly saved item lands.
		createUnsortedCollection(t, ctx, db, userID)

		accessToken := newTestAccessToken(t, userID)

		for range 2 {
			req := httptest.NewRequest(
				http.MethodPost,
				"/saved-items",
				strings.NewReader(`{
					"url": "https://example.com/duplicated"
				}`),
			)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+accessToken)

			resp, err := testApp.Test(req)
			require.NoError(t, err)
			require.Equal(t, http.StatusCreated, resp.StatusCode)
		}

		count, err := db.CountSavedItemsForUser(ctx, userID)
		require.NoError(t, err)
		require.Equal(t, int64(2), count)
	})

	t.Run("normalizes the stored domain", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-domain@example.com",
				DisplayName: "Saved Item Domain User",
			},
		)
		require.NoError(t, err)

		// Migration 000022 seeds one Unsorted collection per user, and that
		// is where a newly saved item lands.
		createUnsortedCollection(t, ctx, db, userID)

		accessToken := newTestAccessToken(t, userID)

		cases := []struct {
			rawURL   string
			expected string
		}{
			{rawURL: "https://www.tiktok.com/@user/video/1", expected: "tiktok.com"},
			{rawURL: "https://m.youtube.com/watch?v=abc", expected: "m.youtube.com"},
			{rawURL: "https://WWW.YouTube.COM/watch?v=abc", expected: "youtube.com"},
			{rawURL: "https://youtu.be/abc", expected: "youtu.be"},
			{rawURL: "https://www.pinterest.com/pin/1", expected: "pinterest.com"},
			{rawURL: "https://notyoutube.com/watch?v=abc", expected: "notyoutube.com"},
		}

		for i, tc := range cases {
			req := httptest.NewRequest(
				http.MethodPost,
				"/saved-items",
				strings.NewReader(`{"url": "`+tc.rawURL+`"}`),
			)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+accessToken)

			resp, err := testApp.Test(req)
			require.NoError(t, err)
			require.Equal(t, http.StatusCreated, resp.StatusCode, tc.rawURL)

			var body struct {
				Data struct {
					SavedItem struct {
						ID       string  `json:"id"`
						URL      string  `json:"url"`
						Domain   *string `json:"domain"`
						Platform *string `json:"platform"`
					} `json:"saved_item"`
				} `json:"data"`
			}

			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
			require.Equal(t, tc.rawURL, body.Data.SavedItem.URL)
			require.NotNil(t, body.Data.SavedItem.Domain)
			require.Equal(
				t,
				tc.expected,
				*body.Data.SavedItem.Domain,
				tc.rawURL,
			)

			// platform is not derived at save time: nothing infers it from
			// the hostname, so it is null until enrichment runs.
			require.Nil(t, body.Data.SavedItem.Platform, tc.rawURL)

			state, err := db.GetSavedItemState(
				ctx,
				mustParseUUID(t, body.Data.SavedItem.ID),
			)
			require.NoError(t, err)
			require.Equal(t, tc.expected, state.Domain.String)
			require.False(t, state.Platform.Valid, tc.rawURL)
			require.Equal(
				t,
				int64(i+1),
				countSavedItemsForUser(t, ctx, db, userID),
			)
		}
	})

	t.Run("ignores a client supplied X-Platform header", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-x-platform@example.com",
				DisplayName: "Saved Item X Platform User",
			},
		)
		require.NoError(t, err)

		// Migration 000022 seeds one Unsorted collection per user, and that
		// is where a newly saved item lands.
		createUnsortedCollection(t, ctx, db, userID)

		accessToken := newTestAccessToken(t, userID)

		req := httptest.NewRequest(
			http.MethodPost,
			"/saved-items",
			strings.NewReader(`{
				"url": "https://www.youtube.com/watch?v=abc"
			}`),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("X-Platform", "android")

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var body struct {
			Data struct {
				SavedItem struct {
					ID       string  `json:"id"`
					Domain   *string `json:"domain"`
					Platform *string `json:"platform"`
				} `json:"saved_item"`
			} `json:"data"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.NotNil(t, body.Data.SavedItem.Domain)
		require.Equal(t, "youtube.com", *body.Data.SavedItem.Domain)
		require.Nil(t, body.Data.SavedItem.Platform)

		state, err := db.GetSavedItemState(
			ctx,
			mustParseUUID(t, body.Data.SavedItem.ID),
		)
		require.NoError(t, err)
		require.False(
			t,
			state.Platform.Valid,
			"saved_items.platform must stay null until enrichment",
		)
	})

	t.Run("rejects request without url", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-missing-url@example.com",
				DisplayName: "Saved Item Missing URL User",
			},
		)
		require.NoError(t, err)

		accessToken := newTestAccessToken(t, userID)

		req := httptest.NewRequest(
			http.MethodPost,
			"/saved-items",
			strings.NewReader(`{}`),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var body struct {
			Error struct {
				Code   string `json:"code"`
				Fields []struct {
					Field string `json:"field"`
				} `json:"fields"`
			} `json:"error"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "VALIDATION_ERROR", body.Error.Code)
		require.Len(t, body.Error.Fields, 1)
		require.Equal(t, "url", body.Error.Fields[0].Field)
	})

	t.Run("rejects request without access token", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		req := httptest.NewRequest(
			http.MethodPost,
			"/saved-items",
			strings.NewReader(`{
				"url": "https://example.com/anonymous"
			}`),
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
}

func TestSavedItem_Get(t *testing.T) {
	t.Run("returns a saved item belonging to the authenticated user", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-get@example.com",
				DisplayName: "Saved Item Get User",
			},
		)
		require.NoError(t, err)

		unsortedCollectionID := createUnsortedCollection(t, ctx, db, userID)

		created, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:       userID,
				Url:          "https://www.youtube.com/watch?v=abc",
				Domain:       testText("youtube.com"),
				Platform:     pgtype.Text{},
				Title:        pgtype.Text{},
				CollectionID: unsortedCollectionID,
				CreatedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
			},
		)
		require.NoError(t, err)

		accessToken := newTestAccessToken(t, userID)

		req := httptest.NewRequest(
			http.MethodGet,
			"/saved-items/"+created.ID.String(),
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var body struct {
			Data struct {
				SavedItem struct {
					ID        string    `json:"id"`
					URL       string    `json:"url"`
					Domain    *string   `json:"domain"`
					Platform  *string   `json:"platform"`
					Title     *string   `json:"title"`
					CreatedAt time.Time `json:"created_at"`
					UpdatedAt time.Time `json:"updated_at"`
				} `json:"saved_item"`
			} `json:"data"`
			Message string `json:"message"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		require.Equal(t, "saved item retrieved successfully", body.Message)
		require.Equal(t, created.ID.String(), body.Data.SavedItem.ID)
		require.Equal(t, "https://www.youtube.com/watch?v=abc", body.Data.SavedItem.URL)
		require.NotNil(t, body.Data.SavedItem.Domain)
		require.Equal(t, "youtube.com", *body.Data.SavedItem.Domain)
		require.Nil(t, body.Data.SavedItem.Platform)
		require.Nil(t, body.Data.SavedItem.Title)
		require.WithinDuration(
			t,
			created.CreatedAt.Time,
			body.Data.SavedItem.CreatedAt,
			time.Second,
		)
		require.WithinDuration(
			t,
			created.UpdatedAt.Time,
			body.Data.SavedItem.UpdatedAt,
			time.Second,
		)
	})

	t.Run("returns the same shape as the list endpoint", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-get-shape@example.com",
				DisplayName: "Saved Item Get Shape User",
			},
		)
		require.NoError(t, err)

		// Migration 000022 seeds one Unsorted collection per user, and that
		// is where a newly saved item lands.
		createUnsortedCollection(t, ctx, db, userID)

		accessToken := newTestAccessToken(t, userID)

		createReq := httptest.NewRequest(
			http.MethodPost,
			"/saved-items",
			strings.NewReader(`{"url": "https://example.com/shape"}`),
		)
		createReq.Header.Set("Content-Type", "application/json")
		createReq.Header.Set("Authorization", "Bearer "+accessToken)

		createResp, err := testApp.Test(createReq)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, createResp.StatusCode)

		var createBody struct {
			Data struct {
				SavedItem struct {
					ID string `json:"id"`
				} `json:"saved_item"`
			} `json:"data"`
		}

		require.NoError(t, json.NewDecoder(createResp.Body).Decode(&createBody))
		require.NotEmpty(t, createBody.Data.SavedItem.ID)

		var detailRaw map[string]any

		getReq := httptest.NewRequest(
			http.MethodGet,
			"/saved-items/"+createBody.Data.SavedItem.ID,
			nil,
		)
		getReq.Header.Set("Authorization", "Bearer "+accessToken)

		getResp, err := testApp.Test(getReq)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, getResp.StatusCode)

		require.NoError(t, json.NewDecoder(getResp.Body).Decode(&detailRaw))

		shapeKeys := detailBodyKeys(t, detailRaw)

		for _, key := range []string{
			"id", "url", "domain", "platform", "title",
			"created_at", "updated_at",
		} {
			require.Contains(t, shapeKeys, key)
		}
		require.Equal(t, 7, len(shapeKeys))
		require.NotContains(t, shapeKeys, "user_id")
	})

	t.Run("returns resource not found for an unknown id", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-get-missing@example.com",
				DisplayName: "Saved Item Get Missing User",
			},
		)
		require.NoError(t, err)

		accessToken := newTestAccessToken(t, userID)

		req := httptest.NewRequest(
			http.MethodGet,
			"/saved-items/"+uuid.New().String(),
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)

		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "RESOURCE_NOT_FOUND", body.Error.Code)
	})

	t.Run("returns resource not found for another user's saved item", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		ownerID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-get-owner@example.com",
				DisplayName: "Saved Item Get Owner User",
			},
		)
		require.NoError(t, err)

		otherUserID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-get-thief@example.com",
				DisplayName: "Saved Item Get Thief User",
			},
		)
		require.NoError(t, err)

		ownerUnsortedCollectionID := createUnsortedCollection(t, ctx, db, ownerID)

		created, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:       ownerID,
				Url:          "https://example.com/someone-elses",
				Domain:       testText("example.com"),
				CollectionID: ownerUnsortedCollectionID,
				CreatedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
			},
		)
		require.NoError(t, err)

		accessToken := newTestAccessToken(t, otherUserID)

		req := httptest.NewRequest(
			http.MethodGet,
			"/saved-items/"+created.ID.String(),
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)

		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "RESOURCE_NOT_FOUND", body.Error.Code)

		// The row still exists and is untouched.
		state, err := db.GetSavedItemState(ctx, created.ID)
		require.NoError(t, err)
		require.Equal(t, ownerID, state.UserID)
	})

	t.Run("bad request responses are identical for unknown and foreign ids", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		ownerID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-get-equal@example.com",
				DisplayName: "Saved Item Get Equal User",
			},
		)
		require.NoError(t, err)

		otherUserID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-get-equal-other@example.com",
				DisplayName: "Saved Item Get Equal Other User",
			},
		)
		require.NoError(t, err)

		ownerUnsortedCollectionID := createUnsortedCollection(t, ctx, db, ownerID)

		created, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:       ownerID,
				Url:          "https://example.com/indistinguishable",
				Domain:       testText("example.com"),
				CollectionID: ownerUnsortedCollectionID,
				CreatedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
			},
		)
		require.NoError(t, err)

		accessToken := newTestAccessToken(t, otherUserID)

		readBody := func(id string) string {
			req := httptest.NewRequest(
				http.MethodGet,
				"/saved-items/"+id,
				nil,
			)
			req.Header.Set("Authorization", "Bearer "+accessToken)

			resp, err := testApp.Test(req)
			require.NoError(t, err)
			require.Equal(t, http.StatusNotFound, resp.StatusCode)

			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			return string(raw)
		}

		require.Equal(
			t,
			readBody(uuid.New().String()),
			readBody(created.ID.String()),
		)
	})

	t.Run("returns bad request for an invalid id", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-get-invalid@example.com",
				DisplayName: "Saved Item Get Invalid User",
			},
		)
		require.NoError(t, err)

		accessToken := newTestAccessToken(t, userID)

		req := httptest.NewRequest(
			http.MethodGet,
			"/saved-items/not-a-uuid",
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+accessToken)

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

	t.Run("returns unauthorized without an access token", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/saved-items/"+uuid.New().String(),
			nil,
		)

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
}

func detailBodyKeys(t *testing.T, raw map[string]any) []string {
	t.Helper()

	data, ok := raw["data"].(map[string]any)
	require.True(t, ok)

	savedItem, ok := data["saved_item"].(map[string]any)
	require.True(t, ok)

	keys := make([]string, 0, len(savedItem))
	for key := range savedItem {
		keys = append(keys, key)
	}

	return keys
}

// deleteSavedItemData is what DELETE /saved-items/:id reports about the
// collection the deleted item was in. It is decoded here rather than reusing the
// API response type so the integration suite asserts the wire shape itself.
type deleteSavedItemData struct {
	CollectionID        string `json:"collection_id"`
	CollectionEmpty     bool   `json:"collection_empty"`
	CollectionDeletable bool   `json:"collection_deletable"`
}

// deleteSavedItem calls the endpoint and returns the decoded collection report.
func deleteSavedItem(
	t *testing.T,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) (int, deleteSavedItemData) {
	t.Helper()

	accessToken := newTestAccessToken(t, userID)

	req := httptest.NewRequest(
		http.MethodDelete,
		"/saved-items/"+savedItemID.String(),
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	var body struct {
		Data    deleteSavedItemData `json:"data"`
		Message string              `json:"message"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	return resp.StatusCode, body.Data
}

// seedSavedItemInCollection files one saved item into a collection and returns
// its id. Used to build collections with a known number of items before deleting
// one of them.
func seedSavedItemInCollection(
	t *testing.T,
	ctx context.Context,
	db *saveditemdbtest.Queries,
	userID uuid.UUID,
	collectionID uuid.UUID,
	url string,
) uuid.UUID {
	t.Helper()

	created, err := db.CreateTestSavedItem(
		ctx,
		saveditemdbtest.CreateTestSavedItemParams{
			UserID:       userID,
			Url:          url,
			Domain:       testText("example.com"),
			CollectionID: collectionID,
			CreatedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
		},
	)
	require.NoError(t, err)

	return created.ID
}

// createUserCollection seeds a collection the user created: type = 'user' with a
// NULL system key.
func createUserCollection(
	t *testing.T,
	ctx context.Context,
	db *saveditemdbtest.Queries,
	userID uuid.UUID,
	name string,
) uuid.UUID {
	t.Helper()

	collectionID, err := db.CreateTestUserCollection(
		ctx,
		saveditemdbtest.CreateTestUserCollectionParams{
			UserID: userID,
			Name:   name,
		},
	)
	require.NoError(t, err)

	return collectionID
}

// createSystemCollection seeds a collection that automatic organization would
// have created: type = 'system' with a key that is not Unsorted.
func createSystemCollection(
	t *testing.T,
	ctx context.Context,
	db *saveditemdbtest.Queries,
	userID uuid.UUID,
	name string,
	systemKey string,
) uuid.UUID {
	t.Helper()

	collectionID, err := db.CreateTestSystemCollection(
		ctx,
		saveditemdbtest.CreateTestSystemCollectionParams{
			UserID:    userID,
			Name:      name,
			SystemKey: testText(systemKey),
		},
	)
	require.NoError(t, err)

	return collectionID
}

// collectionExists reports whether a collection row is still there, which is what
// tells "emptied but kept" apart from "removed".
func collectionExists(
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

// savedItemCollectionID reads an item's current collection, or uuid.Nil when the
// item is gone.
func savedItemCollectionID(
	t *testing.T,
	savedItemID uuid.UUID,
) uuid.UUID {
	t.Helper()

	var collectionID uuid.UUID

	err := testPool.QueryRow(
		context.Background(),
		"SELECT collection_id FROM saved_items WHERE id = $1",
		savedItemID,
	).Scan(&collectionID)

	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil
	}

	require.NoError(t, err)

	return collectionID
}

func TestSavedItem_Delete(t *testing.T) {
	t.Run("deletes a saved item belonging to the authenticated user", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete@example.com",
				DisplayName: "Saved Item Delete User",
			},
		)
		require.NoError(t, err)

		unsortedCollectionID := createUnsortedCollection(t, ctx, db, userID)

		created, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:       userID,
				Url:          "https://example.com/to-be-deleted",
				Domain:       testText("example.com"),
				CollectionID: unsortedCollectionID,
				CreatedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
			},
		)
		require.NoError(t, err)

		accessToken := newTestAccessToken(t, userID)

		req := httptest.NewRequest(
			http.MethodDelete,
			"/saved-items/"+created.ID.String(),
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var body struct {
			Data    deleteSavedItemData `json:"data"`
			Message string              `json:"message"`
		}

		require.NoError(t, json.Unmarshal(raw, &body))
		require.Equal(t, "saved item deleted successfully", body.Message)

		// The item was in Unsorted, which had nothing else in it, so the
		// collection is reported empty and explicitly not deletable: Unsorted is
		// the one collection a user may never remove.
		require.Equal(t, unsortedCollectionID, mustParseUUID(t, body.Data.CollectionID))
		require.True(t, body.Data.CollectionEmpty)
		require.False(t, body.Data.CollectionDeletable)

		require.JSONEq(
			t,
			`{
				"data": {
					"collection_id": "`+unsortedCollectionID.String()+`",
					"collection_empty": true,
					"collection_deletable": false
				},
				"message": "saved item deleted successfully"
			}`,
			string(raw),
		)

		_, err = db.GetSavedItemState(ctx, created.ID)
		require.Error(t, err)
		require.ErrorIs(t, err, pgx.ErrNoRows)

		count, err := db.CountSavedItemsForUser(ctx, userID)
		require.NoError(t, err)
		require.Equal(t, int64(0), count)

		// The collection is reported, never removed. That separation is the whole
		// point of this endpoint.
		require.True(t, collectionExists(t, unsortedCollectionID))
	})

	t.Run("deleting the same item twice returns not found", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-twice@example.com",
				DisplayName: "Saved Item Delete Twice User",
			},
		)
		require.NoError(t, err)

		unsortedCollectionID := createUnsortedCollection(t, ctx, db, userID)

		created, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:       userID,
				Url:          "https://example.com/delete-twice",
				Domain:       testText("example.com"),
				CollectionID: unsortedCollectionID,
				CreatedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
			},
		)
		require.NoError(t, err)

		accessToken := newTestAccessToken(t, userID)

		deleteIt := func() *http.Response {
			req := httptest.NewRequest(
				http.MethodDelete,
				"/saved-items/"+created.ID.String(),
				nil,
			)
			req.Header.Set("Authorization", "Bearer "+accessToken)

			resp, err := testApp.Test(req)
			require.NoError(t, err)

			return resp
		}

		require.Equal(t, http.StatusOK, deleteIt().StatusCode)

		second := deleteIt()
		require.Equal(t, http.StatusNotFound, second.StatusCode)

		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}

		require.NoError(t, json.NewDecoder(second.Body).Decode(&body))
		require.Equal(t, "RESOURCE_NOT_FOUND", body.Error.Code)
	})

	t.Run("returns resource not found for an unknown id", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-missing@example.com",
				DisplayName: "Saved Item Delete Missing User",
			},
		)
		require.NoError(t, err)

		accessToken := newTestAccessToken(t, userID)

		req := httptest.NewRequest(
			http.MethodDelete,
			"/saved-items/"+uuid.New().String(),
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)

		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "RESOURCE_NOT_FOUND", body.Error.Code)
	})

	t.Run("cannot delete another user's saved item and it still exists", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		ownerID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-owner@example.com",
				DisplayName: "Saved Item Delete Owner User",
			},
		)
		require.NoError(t, err)

		otherUserID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-other@example.com",
				DisplayName: "Saved Item Delete Other User",
			},
		)
		require.NoError(t, err)

		ownerUnsortedCollectionID := createUnsortedCollection(t, ctx, db, ownerID)

		created, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:       ownerID,
				Url:          "https://example.com/someone-elses",
				Domain:       testText("example.com"),
				CollectionID: ownerUnsortedCollectionID,
				CreatedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
			},
		)
		require.NoError(t, err)

		accessToken := newTestAccessToken(t, otherUserID)

		req := httptest.NewRequest(
			http.MethodDelete,
			"/saved-items/"+created.ID.String(),
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)

		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "RESOURCE_NOT_FOUND", body.Error.Code)

		state, err := db.GetSavedItemState(ctx, created.ID)
		require.NoError(t, err)
		require.Equal(t, ownerID, state.UserID)
		require.Equal(t, "https://example.com/someone-elses", state.Url)

		count, err := db.CountSavedItemsForUser(ctx, ownerID)
		require.NoError(t, err)
		require.Equal(t, int64(1), count)
	})

	t.Run("not found responses are identical for unknown and foreign ids", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		ownerID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-equal@example.com",
				DisplayName: "Saved Item Delete Equal User",
			},
		)
		require.NoError(t, err)

		otherUserID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-equal-other@example.com",
				DisplayName: "Saved Item Delete Equal Other User",
			},
		)
		require.NoError(t, err)

		ownerUnsortedCollectionID := createUnsortedCollection(t, ctx, db, ownerID)

		created, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:       ownerID,
				Url:          "https://example.com/indistinguishable",
				Domain:       testText("example.com"),
				CollectionID: ownerUnsortedCollectionID,
				CreatedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
			},
		)
		require.NoError(t, err)

		accessToken := newTestAccessToken(t, otherUserID)

		readBody := func(id string) string {
			req := httptest.NewRequest(
				http.MethodDelete,
				"/saved-items/"+id,
				nil,
			)
			req.Header.Set("Authorization", "Bearer "+accessToken)

			resp, err := testApp.Test(req)
			require.NoError(t, err)
			require.Equal(t, http.StatusNotFound, resp.StatusCode)

			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			return string(raw)
		}

		require.Equal(
			t,
			readBody(uuid.New().String()),
			readBody(created.ID.String()),
		)

		// Neither attempt deleted the owner's row.
		_, err = db.GetSavedItemState(ctx, created.ID)
		require.NoError(t, err)
	})

	t.Run("returns bad request for an invalid id", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-invalid@example.com",
				DisplayName: "Saved Item Delete Invalid User",
			},
		)
		require.NoError(t, err)

		accessToken := newTestAccessToken(t, userID)

		req := httptest.NewRequest(
			http.MethodDelete,
			"/saved-items/not-a-uuid",
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+accessToken)

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

	t.Run("returns unauthorized without an access token", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodDelete,
			"/saved-items/"+uuid.New().String(),
			nil,
		)

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

	t.Run("deleted item disappears from the inbox", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-inbox@example.com",
				DisplayName: "Saved Item Delete Inbox User",
			},
		)
		require.NoError(t, err)

		// Migration 000022 seeds one Unsorted collection per user, and that
		// is where a newly saved item lands.
		createUnsortedCollection(t, ctx, db, userID)

		accessToken := newTestAccessToken(t, userID)

		createAndDelete := func(url string) {
			createReq := httptest.NewRequest(
				http.MethodPost,
				"/saved-items",
				strings.NewReader(`{"url": "`+url+`"}`),
			)
			createReq.Header.Set("Content-Type", "application/json")
			createReq.Header.Set("Authorization", "Bearer "+accessToken)

			createResp, err := testApp.Test(createReq)
			require.NoError(t, err)
			require.Equal(t, http.StatusCreated, createResp.StatusCode)

			var created struct {
				Data struct {
					SavedItem struct {
						ID string `json:"id"`
					} `json:"saved_item"`
				} `json:"data"`
			}

			require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))

			deleteReq := httptest.NewRequest(
				http.MethodDelete,
				"/saved-items/"+created.Data.SavedItem.ID,
				nil,
			)
			deleteReq.Header.Set("Authorization", "Bearer "+accessToken)

			deleteResp, err := testApp.Test(deleteReq)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, deleteResp.StatusCode)
		}

		createAndDelete("https://example.com/gone-1")
		createAndDelete("https://example.com/gone-2")

		listReq := httptest.NewRequest(http.MethodGet, "/saved-items", nil)
		listReq.Header.Set("Authorization", "Bearer "+accessToken)

		listResp, err := testApp.Test(listReq)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, listResp.StatusCode)

		var listBody struct {
			Data struct {
				SavedItems []json.RawMessage `json:"saved_items"`
			} `json:"data"`
			Meta struct {
				Pagination struct {
					Total int64 `json:"total"`
				} `json:"pagination"`
			} `json:"meta"`
		}

		require.NoError(t, json.NewDecoder(listResp.Body).Decode(&listBody))
		require.Empty(t, listBody.Data.SavedItems)
		require.Equal(t, int64(0), listBody.Meta.Pagination.Total)
	})
}

// Deleting one of several items reports a collection that is not empty and
// therefore not deletable.
func TestSavedItem_Delete_ReportsCollectionState(t *testing.T) {
	t.Run("a collection with other items is reported not empty", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-multiple@example.com",
				DisplayName: "Saved Item Delete Multiple User",
			},
		)
		require.NoError(t, err)

		createUnsortedCollection(t, ctx, db, userID)

		wishlistID := createUserCollection(t, ctx, db, userID, "Wishlist")

		deleted := seedSavedItemInCollection(
			t, ctx, db, userID, wishlistID, "https://example.com/wishlist/1",
		)

		// A second item is what makes this collection not empty afterwards.
		seedSavedItemInCollection(
			t, ctx, db, userID, wishlistID, "https://example.com/wishlist/2",
		)

		status, data := deleteSavedItem(t, userID, deleted)

		require.Equal(t, http.StatusOK, status)
		require.Equal(t, wishlistID, mustParseUUID(t, data.CollectionID))
		require.False(t, data.CollectionEmpty)
		require.False(t, data.CollectionDeletable)

		require.True(t, collectionExists(t, wishlistID))
		require.Equal(
			t,
			int64(1),
			countSavedItemsInCollection(t, wishlistID),
		)
	})

	t.Run("the last item of a user collection makes it empty and deletable", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-last-user@example.com",
				DisplayName: "Saved Item Delete Last User",
			},
		)
		require.NoError(t, err)

		createUnsortedCollection(t, ctx, db, userID)

		wishlistID := createUserCollection(t, ctx, db, userID, "Wishlist")

		only := seedSavedItemInCollection(
			t, ctx, db, userID, wishlistID, "https://example.com/wishlist/only",
		)

		status, data := deleteSavedItem(t, userID, only)

		require.Equal(t, http.StatusOK, status)
		require.Equal(t, wishlistID, mustParseUUID(t, data.CollectionID))
		require.True(t, data.CollectionEmpty)
		require.True(
			t,
			data.CollectionDeletable,
			"a user collection emptied by deleting its last item is one the user may delete",
		)

		// Reported, not removed.
		require.True(t, collectionExists(t, wishlistID))
		require.Equal(t, int64(0), countSavedItemsInCollection(t, wishlistID))
	})

	// Type is not the criterion. A collection automatic organization created is
	// one the user may delete exactly like one they named themselves, because
	// 'system' says who created it, not what it is.
	t.Run("the last item of a system collection is deletable too", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-system@example.com",
				DisplayName: "Saved Item Delete System User",
			},
		)
		require.NoError(t, err)

		createUnsortedCollection(t, ctx, db, userID)

		youtubeID := createSystemCollection(
			t, ctx, db, userID, "YouTube", "youtube",
		)

		only := seedSavedItemInCollection(
			t, ctx, db, userID, youtubeID, "https://example.com/watch?v=1",
		)

		status, data := deleteSavedItem(t, userID, only)

		require.Equal(t, http.StatusOK, status)
		require.Equal(t, youtubeID, mustParseUUID(t, data.CollectionID))
		require.True(t, data.CollectionEmpty)
		require.True(t, data.CollectionDeletable)

		require.True(t, collectionExists(t, youtubeID))
	})

	// Unsorted is the single exception, and it is recognised by its stable key.
	t.Run("an emptied Unsorted collection is empty but never deletable", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-unsorted-only@example.com",
				DisplayName: "Saved Item Delete Unsorted Only User",
			},
		)
		require.NoError(t, err)

		unsortedID := createUnsortedCollection(t, ctx, db, userID)

		only := seedSavedItemInCollection(
			t, ctx, db, userID, unsortedID, "https://example.com/only-in-inbox",
		)

		status, data := deleteSavedItem(t, userID, only)

		require.Equal(t, http.StatusOK, status)
		require.Equal(t, unsortedID, mustParseUUID(t, data.CollectionID))
		require.True(t, data.CollectionEmpty)
		require.False(
			t,
			data.CollectionDeletable,
			"Unsorted is the one collection a user may never delete",
		)

		require.True(t, collectionExists(t, unsortedID))
	})

	t.Run("Unsorted with other items is reported not empty", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-unsorted-many@example.com",
				DisplayName: "Saved Item Delete Unsorted Many User",
			},
		)
		require.NoError(t, err)

		unsortedID := createUnsortedCollection(t, ctx, db, userID)

		deleted := seedSavedItemInCollection(
			t, ctx, db, userID, unsortedID, "https://example.com/inbox/1",
		)

		seedSavedItemInCollection(
			t, ctx, db, userID, unsortedID, "https://example.com/inbox/2",
		)

		status, data := deleteSavedItem(t, userID, deleted)

		require.Equal(t, http.StatusOK, status)
		require.Equal(t, unsortedID, mustParseUUID(t, data.CollectionID))
		require.False(t, data.CollectionEmpty)
		require.False(t, data.CollectionDeletable)

		require.True(t, collectionExists(t, unsortedID))
	})

	// The reported collection must be the deleted item's own, not one read
	// beforehand. An item filed into one collection after being created in
	// another is the case where the two answers differ.
	t.Run("the reported collection is the deleted item's own", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-moved@example.com",
				DisplayName: "Saved Item Delete Moved User",
			},
		)
		require.NoError(t, err)

		unsortedID := createUnsortedCollection(t, ctx, db, userID)
		wishlistID := createUserCollection(t, ctx, db, userID, "Wishlist")

		item := seedSavedItemInCollection(
			t, ctx, db, userID, unsortedID, "https://example.com/moved-then-deleted",
		)

		// File it through the real collection endpoint, so the item's collection is
		// changed by the same path a user would change it.
		svc := newCollectionService(t)

		_, err = svc.PutSavedItem(ctx, userID, item, "Wishlist")
		require.NoError(t, err)

		require.Equal(t, wishlistID, savedItemCollectionID(t, item))

		status, data := deleteSavedItem(t, userID, item)

		require.Equal(t, http.StatusOK, status)
		require.Equal(
			t,
			wishlistID,
			mustParseUUID(t, data.CollectionID),
			"the report must name the collection the item was actually in",
		)

		// Unsorted was left empty by the move and is not what the report names.
		require.True(t, collectionExists(t, unsortedID))
		require.Equal(t, int64(0), countSavedItemsInCollection(t, unsortedID))
		require.True(t, collectionExists(t, wishlistID))
	})

	// A foreign item fails on the delete, so the collection is never read and the
	// owner's collection is untouched.
	t.Run("a foreign item reports nothing and leaves the collection alone", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		ownerID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-report-owner@example.com",
				DisplayName: "Saved Item Delete Report Owner User",
			},
		)
		require.NoError(t, err)

		otherUserID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-report-other@example.com",
				DisplayName: "Saved Item Delete Report Other User",
			},
		)
		require.NoError(t, err)

		createUnsortedCollection(t, ctx, db, ownerID)
		createUnsortedCollection(t, ctx, db, otherUserID)

		ownerWishlistID := createUserCollection(t, ctx, db, ownerID, "Wishlist")

		item := seedSavedItemInCollection(
			t, ctx, db, ownerID, ownerWishlistID, "https://example.com/owners",
		)

		// Another user's collection with no items, which would be reported
		// deletable if the ownership scoping on the collection reads were absent.
		otherWishlistID := createUserCollection(t, ctx, db, otherUserID, "Wishlist")

		status, data := deleteSavedItem(t, otherUserID, item)

		require.Equal(t, http.StatusNotFound, status)
		require.Equal(t, deleteSavedItemData{}, data)

		require.Equal(t, ownerWishlistID, savedItemCollectionID(t, item))
		require.Equal(t, int64(1), countSavedItemsInCollection(t, ownerWishlistID))
		require.True(t, collectionExists(t, ownerWishlistID))
		require.True(t, collectionExists(t, otherWishlistID))
	})
}

// Concurrency coverage asserts invariants, because PostgreSQL decides the order
// and no particular interleaving is guaranteed.
func TestSavedItem_Delete_Concurrency(t *testing.T) {
	// Two clients deleting the last two items of one collection must both succeed
	// and must never corrupt the collection. Both may be told the collection is
	// empty, because each count is its own snapshot and both deletes can commit
	// first; the assertions below cover the invariants that hold either way.
	t.Run("concurrent deletes of the last two items stay consistent", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-concurrent@example.com",
				DisplayName: "Saved Item Delete Concurrent User",
			},
		)
		require.NoError(t, err)

		createUnsortedCollection(t, ctx, db, userID)

		wishlistID := createUserCollection(t, ctx, db, userID, "Wishlist")

		first := seedSavedItemInCollection(
			t, ctx, db, userID, wishlistID, "https://example.com/race/1",
		)
		second := seedSavedItemInCollection(
			t, ctx, db, userID, wishlistID, "https://example.com/race/2",
		)

		const callers = 2

		type outcome struct {
			status int
			data   deleteSavedItemData
		}

		results := make([]outcome, callers)

		var wg sync.WaitGroup

		for i, itemID := range []uuid.UUID{first, second} {
			wg.Add(1)

			go func(index int, id uuid.UUID) {
				defer wg.Done()

				status, data := deleteSavedItem(t, userID, id)
				results[index] = outcome{status: status, data: data}
			}(i, itemID)
		}

		wg.Wait()

		for i, res := range results {
			require.Equal(
				t,
				http.StatusOK,
				res.status,
				"caller %d must succeed",
				i,
			)

			// Both reports name the one collection, which is the invariant that
			// matters: no caller was told about a collection it did not delete from.
			require.Equal(
				t,
				wishlistID,
				mustParseUUID(t, res.data.CollectionID),
				"caller %d was told about the wrong collection",
				i,
			)

			// The report is a conjunction, so a caller can never be told a collection
			// is deletable without also being told it is empty.
			if res.data.CollectionDeletable {
				require.True(
					t,
					res.data.CollectionEmpty,
					"caller %d was told an emptier state that contradicts the other",
					i,
				)
			}
		}

		// Both callers may report the collection empty, and that is correct rather
		// than a race defect. Each count is a separate statement taking its own
		// snapshot under READ COMMITTED, so when both deletes commit before either
		// count runs, both observe zero and both are right.
		//
		// A caller reporting "not empty" is the only stale answer possible, and it
		// merely means that caller skips an optional cleanup. No interleaving can
		// produce a wrong collection id, a lost delete, or a collection that is not
		// really in the state reported.
		require.Equal(
			t,
			int64(0),
			countSavedItemsInCollection(t, wishlistID),
			"the collection really is empty, whatever the reports said",
		)

		// Both rows are gone, the collection is intact and empty, and the report
		// never touched it.
		require.Equal(t, int64(0), countSavedItemsInCollection(t, wishlistID))
		require.True(t, collectionExists(t, wishlistID))
		require.Equal(t, int64(0), countSavedItemsForUser(t, ctx, db, userID))
	})

	// The same item deleted twice concurrently is one success and one not found,
	// exactly as deleting it twice in sequence is. The delete statement itself
	// serializes the two, so the loser finds no row.
	t.Run("concurrent duplicate deletes give one success and one not found", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-delete-duplicate@example.com",
				DisplayName: "Saved Item Delete Duplicate User",
			},
		)
		require.NoError(t, err)

		createUnsortedCollection(t, ctx, db, userID)

		wishlistID := createUserCollection(t, ctx, db, userID, "Wishlist")

		item := seedSavedItemInCollection(
			t, ctx, db, userID, wishlistID, "https://example.com/duplicate",
		)

		const callers = 2

		statuses := make([]int, callers)
		reports := make([]deleteSavedItemData, callers)

		var wg sync.WaitGroup

		for i := range callers {
			wg.Add(1)

			go func(index int) {
				defer wg.Done()

				status, data := deleteSavedItem(t, userID, item)
				statuses[index] = status
				reports[index] = data
			}(i)
		}

		wg.Wait()

		successes := 0
		notFounds := 0

		for i, status := range statuses {
			switch status {
			case http.StatusOK:
				successes++

				// Exactly the caller that deleted the row is told anything at all.
				require.Equal(t, wishlistID, mustParseUUID(t, reports[i].CollectionID))
			case http.StatusNotFound:
				notFounds++

				// The loser must report nothing: it deleted no row, so it has no
				// collection to describe.
				require.Equal(t, deleteSavedItemData{}, reports[i])
			default:
				t.Fatalf("caller %d returned unexpected status %d", i, status)
			}
		}

		require.Equal(t, 1, successes)
		require.Equal(t, 1, notFounds)

		require.Equal(t, int64(0), countSavedItemsInCollection(t, wishlistID))
		require.True(t, collectionExists(t, wishlistID))
	})
}

func TestSavedItem_List(t *testing.T) {
	t.Run("returns the current user's items newest first", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-list@example.com",
				DisplayName: "Saved Item List User",
			},
		)
		require.NoError(t, err)

		otherUserID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-list-other@example.com",
				DisplayName: "Saved Item List Other User",
			},
		)
		require.NoError(t, err)

		base := time.Now().Add(-time.Hour)

		unsortedCollectionID := createUnsortedCollection(t, ctx, db, userID)

		otherUnsortedCollectionID := createUnsortedCollection(t, ctx, db, otherUserID)

		older, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:       userID,
				Url:          "https://example.com/older",
				Domain:       testText("example.com"),
				Platform:     testText("web"),
				CollectionID: unsortedCollectionID,
				CreatedAt:    pgtype.Timestamptz{Time: base, Valid: true},
			},
		)
		require.NoError(t, err)

		newer, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:       userID,
				Url:          "https://example.com/newer",
				Domain:       testText("example.com"),
				Platform:     testText("web"),
				CollectionID: unsortedCollectionID,
				CreatedAt: pgtype.Timestamptz{
					Time:  base.Add(30 * time.Minute),
					Valid: true,
				},
			},
		)
		require.NoError(t, err)

		_, err = db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:       otherUserID,
				Url:          "https://other.example.com/secret",
				Domain:       testText("other.example.com"),
				Platform:     testText("web"),
				CollectionID: otherUnsortedCollectionID,
				CreatedAt: pgtype.Timestamptz{
					Time:  base.Add(45 * time.Minute),
					Valid: true,
				},
			},
		)
		require.NoError(t, err)

		accessToken := newTestAccessToken(t, userID)

		req := httptest.NewRequest(
			http.MethodGet,
			"/saved-items",
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var body struct {
			Data struct {
				SavedItems []struct {
					ID  string `json:"id"`
					URL string `json:"url"`
				} `json:"saved_items"`
			} `json:"data"`
			Meta struct {
				Pagination struct {
					Page       int   `json:"page"`
					Limit      int   `json:"limit"`
					Total      int64 `json:"total"`
					TotalPages int   `json:"total_pages"`
				} `json:"pagination"`
			} `json:"meta"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		require.Len(t, body.Data.SavedItems, 2)
		require.Equal(t, newer.ID.String(), body.Data.SavedItems[0].ID)
		require.Equal(t, older.ID.String(), body.Data.SavedItems[1].ID)
		require.Equal(t, int64(2), body.Meta.Pagination.Total)
		require.Equal(t, 1, body.Meta.Pagination.Page)
		require.Equal(t, 20, body.Meta.Pagination.Limit)
		require.Equal(t, 1, body.Meta.Pagination.TotalPages)
	})

	t.Run("returns an empty inbox", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-empty@example.com",
				DisplayName: "Saved Item Empty User",
			},
		)
		require.NoError(t, err)

		accessToken := newTestAccessToken(t, userID)

		req := httptest.NewRequest(
			http.MethodGet,
			"/saved-items",
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var body struct {
			Data struct {
				SavedItems []json.RawMessage `json:"saved_items"`
			} `json:"data"`
			Meta struct {
				Pagination struct {
					Total      int64 `json:"total"`
					TotalPages int   `json:"total_pages"`
				} `json:"pagination"`
			} `json:"meta"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Empty(t, body.Data.SavedItems)
		require.NotNil(t, body.Data.SavedItems)
		require.Equal(t, int64(0), body.Meta.Pagination.Total)
		require.Equal(t, 0, body.Meta.Pagination.TotalPages)
	})

	t.Run("paginates", func(t *testing.T) {
		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		require.NoError(t, db.TruncateSavedItemData(ctx))

		userID, err := db.CreateSavedItemUser(
			ctx,
			saveditemdbtest.CreateSavedItemUserParams{
				Email:       "saved-item-pagination@example.com",
				DisplayName: "Saved Item Pagination User",
			},
		)
		require.NoError(t, err)

		base := time.Now().Add(-time.Hour)

		unsortedCollectionID := createUnsortedCollection(t, ctx, db, userID)

		for i := range 3 {
			_, err := db.CreateTestSavedItem(
				ctx,
				saveditemdbtest.CreateTestSavedItemParams{
					UserID:       userID,
					Url:          "https://example.com/page/" + string(rune('a'+i)),
					Domain:       testText("example.com"),
					Platform:     testText("web"),
					CollectionID: unsortedCollectionID,
					CreatedAt: pgtype.Timestamptz{
						Time:  base.Add(time.Duration(i) * time.Minute),
						Valid: true,
					},
				},
			)
			require.NoError(t, err)
		}

		accessToken := newTestAccessToken(t, userID)

		req := httptest.NewRequest(
			http.MethodGet,
			"/saved-items?page=2&limit=1",
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var body struct {
			Data struct {
				SavedItems []struct {
					ID string `json:"id"`
				} `json:"saved_items"`
			} `json:"data"`
			Meta struct {
				Pagination struct {
					Page       int   `json:"page"`
					Limit      int   `json:"limit"`
					Total      int64 `json:"total"`
					TotalPages int   `json:"total_pages"`
				} `json:"pagination"`
			} `json:"meta"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		require.Len(t, body.Data.SavedItems, 1)
		require.Equal(t, 2, body.Meta.Pagination.Page)
		require.Equal(t, 1, body.Meta.Pagination.Limit)
		require.Equal(t, int64(3), body.Meta.Pagination.Total)
		require.Equal(t, 3, body.Meta.Pagination.TotalPages)
	})

	t.Run("rejects request without access token", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/saved-items",
			nil,
		)

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
}
