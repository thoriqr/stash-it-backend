package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

		accessToken := newTestAccessToken(t, userID)

		cases := []struct {
			rawURL   string
			expected string
		}{
			{rawURL: "https://www.tiktok.com/@user/video/1", expected: "tiktok.com"},
			{rawURL: "https://m.youtube.com/watch?v=abc", expected: "m.youtube.com"},
			{rawURL: "https://WWW.YouTube.COM/watch?v=abc", expected: "youtube.com"},
			{rawURL: "https://www.pinterest.com/pin/1", expected: "pinterest.com"},
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
						ID     string  `json:"id"`
						URL    string  `json:"url"`
						Domain *string `json:"domain"`
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

			state, err := db.GetSavedItemState(
				ctx,
				mustParseUUID(t, body.Data.SavedItem.ID),
			)
			require.NoError(t, err)
			require.Equal(t, tc.expected, state.Domain.String)
			require.False(t, state.Platform.Valid)
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
			"saved_items.platform must stay null in Phase A",
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

		created, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:    userID,
				Url:       "https://www.youtube.com/watch?v=abc",
				Domain:    testText("youtube.com"),
				Platform:  pgtype.Text{},
				Title:     pgtype.Text{},
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
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
					ID        string     `json:"id"`
					URL       string     `json:"url"`
					Domain    *string    `json:"domain"`
					Platform  *string    `json:"platform"`
					Title     *string    `json:"title"`
					CreatedAt time.Time  `json:"created_at"`
					UpdatedAt time.Time  `json:"updated_at"`
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

		created, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:    ownerID,
				Url:       "https://example.com/someone-elses",
				Domain:    testText("example.com"),
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
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

		created, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:    ownerID,
				Url:       "https://example.com/indistinguishable",
				Domain:    testText("example.com"),
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
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

		created, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:    userID,
				Url:       "https://example.com/to-be-deleted",
				Domain:    testText("example.com"),
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
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
			Data    any    `json:"data"`
			Message string `json:"message"`
		}

		require.NoError(t, json.Unmarshal(raw, &body))
		require.Equal(t, "saved item deleted successfully", body.Message)
		require.Nil(t, body.Data)
		require.JSONEq(t, `{"data":null,"message":"saved item deleted successfully"}`, string(raw))

		_, err = db.GetSavedItemState(ctx, created.ID)
		require.Error(t, err)
		require.ErrorIs(t, err, pgx.ErrNoRows)

		count, err := db.CountSavedItemsForUser(ctx, userID)
		require.NoError(t, err)
		require.Equal(t, int64(0), count)
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

		created, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:    userID,
				Url:       "https://example.com/delete-twice",
				Domain:    testText("example.com"),
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
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

		created, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:    ownerID,
				Url:       "https://example.com/someone-elses",
				Domain:    testText("example.com"),
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
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

		created, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:    ownerID,
				Url:       "https://example.com/indistinguishable",
				Domain:    testText("example.com"),
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
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

		older, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:    userID,
				Url:       "https://example.com/older",
				Domain:    testText("example.com"),
				Platform:  testText("web"),
				CreatedAt: pgtype.Timestamptz{Time: base, Valid: true},
			},
		)
		require.NoError(t, err)

		newer, err := db.CreateTestSavedItem(
			ctx,
			saveditemdbtest.CreateTestSavedItemParams{
				UserID:    userID,
				Url:       "https://example.com/newer",
				Domain:    testText("example.com"),
				Platform:  testText("web"),
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
				UserID:    otherUserID,
				Url:       "https://other.example.com/secret",
				Domain:    testText("other.example.com"),
				Platform:  testText("web"),
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

		for i := range 3 {
			_, err := db.CreateTestSavedItem(
				ctx,
				saveditemdbtest.CreateTestSavedItemParams{
					UserID:    userID,
					Url:       "https://example.com/page/" + string(rune('a'+i)),
					Domain:    testText("example.com"),
					Platform:  testText("web"),
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

