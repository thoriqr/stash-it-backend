package collection_test

import (
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

	"github.com/thoriqr/stash-it-backend/internal/api/collection"
	collectiondb "github.com/thoriqr/stash-it-backend/internal/api/collection/generated"
	collectionmocks "github.com/thoriqr/stash-it-backend/internal/api/collection/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/validation"
)

const handlerTestAccessTokenSecret = "collection-handler-test-secret"

// newHandlerTestApp builds the real handler, wired to a mock repository, behind the
// real auth middleware. Binding, path parsing, status codes and error
// pass-through are all exercised; only the repository is substituted, because the
// repository's transaction needs a real database and is covered by the
// integration tests instead.
func newHandlerTestApp(
	t *testing.T,
	repo collection.Repository,
) *fiber.App {
	t.Helper()

	app := fiber.New(fiber.Config{
		ErrorHandler:    httpx.NewErrorHandler(zap.NewNop()),
		StructValidator: validation.New(),
	})

	collection.Routes(
		app.Group("/saved-items"),
		collection.NewHandler(collection.NewService(repo)),
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

func putSavedItemRequest(
	t *testing.T,
	userID uuid.UUID,
	savedItemID uuid.UUID,
	body string,
	authorize bool,
) *http.Request {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPut,
		"/saved-items/"+savedItemID.String()+"/collection",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")

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

type putSavedItemPayload struct {
	Message string `json:"message"`
	Data    struct {
		Collection struct {
			ID        string    `json:"id"`
			Name      string    `json:"name"`
			Type      string    `json:"type"`
			CreatedAt time.Time `json:"created_at"`
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"collection"`
		SavedItem struct {
			ID           string    `json:"id"`
			URL          string    `json:"url"`
			Domain       *string   `json:"domain"`
			Platform     *string   `json:"platform"`
			Title        *string   `json:"title"`
			CollectionID string    `json:"collection_id"`
			CreatedAt    time.Time `json:"created_at"`
			UpdatedAt    time.Time `json:"updated_at"`
		} `json:"saved_item"`
		CollectionCreated   bool `json:"collection_created"`
		AlreadyInCollection bool `json:"already_in_collection"`
	} `json:"data"`
}

func decodePayload(t *testing.T, resp *http.Response) putSavedItemPayload {
	t.Helper()

	var payload putSavedItemPayload

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))

	return payload
}

func handlerTimestamptz() pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
}

func handlerPgText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}

func TestHandler_PutSavedItem(t *testing.T) {
	t.Run("moves the saved item into the collection and returns 200", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		userID := uuid.New()
		savedItemID := uuid.New()
		collectionID := uuid.New()

		repo.EXPECT().
			PutSavedItemIntoUserCollection(
				gomock.Any(),
				collection.PutSavedItemIntoUserCollectionParams{
					UserID:      userID,
					SavedItemID: savedItemID,
					Name:        "Wishlist",
				},
			).
			Return(
				collection.PutSavedItemIntoUserCollectionResult{
					Collection: collectiondb.Collection{
						ID:        collectionID,
						UserID:    userID,
						Name:      "Wishlist",
						Type:      "user",
						CreatedAt: handlerTimestamptz(),
						UpdatedAt: handlerTimestamptz(),
					},
					CollectionCreated: true,
					SavedItem: collection.SavedItem{
						ID:           savedItemID,
						UserID:       userID,
						URL:          "https://example.com/articles/1",
						Domain:       handlerPgText("example.com"),
						CollectionID: collectionID,
						CreatedAt:    handlerTimestamptz(),
						UpdatedAt:    handlerTimestamptz(),
					},
				},
				nil,
			)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(
			putSavedItemRequest(
				t,
				userID,
				savedItemID,
				`{"collection_name": "Wishlist"}`,
				true,
			),
		)
		require.NoError(t, err)

		// 200, not 201: the call succeeds whether or not it created anything, and
		// the payload reports which happened.
		require.Equal(t, http.StatusOK, resp.StatusCode)

		payload := decodePayload(t, resp)

		require.Equal(
			t,
			"saved item moved into collection successfully",
			payload.Message,
		)
		require.Equal(t, collectionID.String(), payload.Data.Collection.ID)
		require.Equal(t, "Wishlist", payload.Data.Collection.Name)
		require.Equal(t, "user", payload.Data.Collection.Type)
		require.Equal(t, savedItemID.String(), payload.Data.SavedItem.ID)
		require.Equal(
			t,
			collectionID.String(),
			payload.Data.SavedItem.CollectionID,
		)
		require.Equal(t, "example.com", *payload.Data.SavedItem.Domain)
		require.True(t, payload.Data.CollectionCreated)
		require.False(t, payload.Data.AlreadyInCollection)
	})

	t.Run("does not expose a system key on the collection", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		userID := uuid.New()
		savedItemID := uuid.New()

		repo.EXPECT().
			PutSavedItemIntoUserCollection(gomock.Any(), gomock.Any()).
			Return(
				collection.PutSavedItemIntoUserCollectionResult{
					Collection: collectiondb.Collection{
						ID:        uuid.New(),
						UserID:    userID,
						Name:      "Wishlist",
						Type:      "user",
						SystemKey: pgtype.Text{String: "unsorted", Valid: true},
						CreatedAt: handlerTimestamptz(),
						UpdatedAt: handlerTimestamptz(),
					},
					CollectionCreated: true,
					SavedItem: collection.SavedItem{
						ID:           savedItemID,
						UserID:       userID,
						CollectionID: uuid.New(),
						CreatedAt:    handlerTimestamptz(),
						UpdatedAt:    handlerTimestamptz(),
					},
				},
				nil,
			)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(
			putSavedItemRequest(
				t,
				userID,
				savedItemID,
				`{"collection_name": "Wishlist"}`,
				true,
			),
		)
		require.NoError(t, err)

		// This endpoint only ever targets a user collection, so the system key must
		// not leak into the contract even if a row carried one.
		require.NotContains(t, readBody(t, resp), "system_key")
		require.NotContains(t, readBody(t, resp), "unsorted")
	})

	t.Run("omits enrichment and null metadata from the payload", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		userID := uuid.New()
		savedItemID := uuid.New()

		repo.EXPECT().
			PutSavedItemIntoUserCollection(gomock.Any(), gomock.Any()).
			Return(
				collection.PutSavedItemIntoUserCollectionResult{
					Collection: collectiondb.Collection{
						ID:        uuid.New(),
						UserID:    userID,
						Name:      "Wishlist",
						Type:      "user",
						CreatedAt: handlerTimestamptz(),
						UpdatedAt: handlerTimestamptz(),
					},
					CollectionCreated: true,
					SavedItem: collection.SavedItem{
						ID:        savedItemID,
						UserID:    userID,
						URL:       "https://example.com/articles/1",
						Domain:    pgtype.Text{},
						Platform:  pgtype.Text{},
						Title:     pgtype.Text{},
						CreatedAt: handlerTimestamptz(),
						UpdatedAt: handlerTimestamptz(),
					},
				},
				nil,
			)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(
			putSavedItemRequest(
				t,
				userID,
				savedItemID,
				`{"collection_name": "Wishlist"}`,
				true,
			),
		)
		require.NoError(t, err)

		raw := readBody(t, resp)

		// Enrichment is not part of this operation, so no enrichment field may appear.
		require.NotContains(t, raw, "enrichment")
		require.NotContains(t, raw, "last_enriched")

		// A null optional field is present and explicitly null, matching the
		// saved_item contract rather than being dropped.
		require.Contains(t, raw, `"platform":null`)
		require.Contains(t, raw, `"title":null`)
	})

	t.Run("reports both flags when the item was already filed", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		userID := uuid.New()
		savedItemID := uuid.New()
		collectionID := uuid.New()

		repo.EXPECT().
			PutSavedItemIntoUserCollection(gomock.Any(), gomock.Any()).
			Return(
				collection.PutSavedItemIntoUserCollectionResult{
					Collection: collectiondb.Collection{
						ID:        collectionID,
						UserID:    userID,
						Name:      "Wishlist",
						Type:      "user",
						CreatedAt: handlerTimestamptz(),
						UpdatedAt: handlerTimestamptz(),
					},
					CollectionCreated:   false,
					AlreadyInCollection: true,
					SavedItem: collection.SavedItem{
						ID:           savedItemID,
						UserID:       userID,
						CollectionID: collectionID,
						CreatedAt:    handlerTimestamptz(),
						UpdatedAt:    handlerTimestamptz(),
					},
				},
				nil,
			)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(
			putSavedItemRequest(
				t,
				userID,
				savedItemID,
				`{"collection_name": "Wishlist"}`,
				true,
			),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		payload := decodePayload(t, resp)
		require.False(t, payload.Data.CollectionCreated)
		require.True(t, payload.Data.AlreadyInCollection)
	})

	t.Run("forwards the trimmed name from the body", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		userID := uuid.New()
		savedItemID := uuid.New()

		repo.EXPECT().
			PutSavedItemIntoUserCollection(
				gomock.Any(),
				collection.PutSavedItemIntoUserCollectionParams{
					UserID:      userID,
					SavedItemID: savedItemID,
					Name:        "Wishlist",
				},
			).
			Return(
				collection.PutSavedItemIntoUserCollectionResult{
					Collection: collectiondb.Collection{
						ID:        uuid.New(),
						UserID:    userID,
						Name:      "Wishlist",
						Type:      "user",
						CreatedAt: handlerTimestamptz(),
						UpdatedAt: handlerTimestamptz(),
					},
					CollectionCreated: true,
					SavedItem: collection.SavedItem{
						ID:        savedItemID,
						UserID:    userID,
						CreatedAt: handlerTimestamptz(),
						UpdatedAt: handlerTimestamptz(),
					},
				},
				nil,
			)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(
			putSavedItemRequest(
				t,
				userID,
				savedItemID,
				`{"collection_name": "  Wishlist  "}`,
				true,
			),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("returns the reserved name conflict unchanged", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		userID := uuid.New()
		savedItemID := uuid.New()

		repo.EXPECT().
			PutSavedItemIntoUserCollection(gomock.Any(), gomock.Any()).
			Return(
				collection.PutSavedItemIntoUserCollectionResult{},
				apperror.ConflictWith(
					collection.CodeCollectionNameReserved,
					"collection name is already in use",
					nil,
				),
			)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(
			putSavedItemRequest(
				t,
				userID,
				savedItemID,
				`{"collection_name": "Unsorted"}`,
				true,
			),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusConflict, resp.StatusCode)

		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(
			t,
			collection.CodeCollectionNameReserved,
			body.Error.Code,
		)
	})

	t.Run("rejects a blank name without touching the repository", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		userID := uuid.New()
		savedItemID := uuid.New()

		// The service rejects a blank name before the repository is called.
		repo.EXPECT().
			PutSavedItemIntoUserCollection(gomock.Any(), gomock.Any()).
			Times(0)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(
			putSavedItemRequest(t, userID, savedItemID, `{"collection_name": "   "}`, true),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		// The service's own code, not a generic validation error.
		require.Equal(
			t,
			collection.CodeInvalidCollectionName,
			body.Error.Code,
		)
	})

	t.Run("not found responses are identical for unknown and foreign ids", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		userID := uuid.New()

		// Both ids produce the same error, so the handler cannot distinguish them.
		repo.EXPECT().
			PutSavedItemIntoUserCollection(gomock.Any(), gomock.Any()).
			Return(
				collection.PutSavedItemIntoUserCollectionResult{},
				apperror.NotFoundWith(
					collection.CodeSavedItemNotFound,
					"saved item not found",
					nil,
				),
			).
			Times(2)

		app := newHandlerTestApp(t, repo)

		put := func(savedItemID uuid.UUID) string {
			resp, err := app.Test(
				putSavedItemRequest(
					t,
					userID,
					savedItemID,
					`{"collection_name": "Wishlist"}`,
					true,
				),
			)
			require.NoError(t, err)
			require.Equal(t, http.StatusNotFound, resp.StatusCode)

			return readBody(t, resp)
		}

		require.Equal(t, put(uuid.New()), put(uuid.New()))
	})

	t.Run("returns bad request for an invalid saved item id", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		app := newHandlerTestApp(t, repo)

		req := putSavedItemRequest(
			t,
			uuid.New(),
			uuid.Nil,
			`{"collection_name": "Wishlist"}`,
			true,
		)
		req.URL.Path = "/saved-items/not-a-uuid/collection"

		resp, err := app.Test(req)
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
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(
			putSavedItemRequest(
				t,
				uuid.New(),
				uuid.New(),
				`{"collection_name": "Wishlist"}`,
				false,
			),
		)
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

	t.Run("returns unauthorized for an invalid access token", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		app := newHandlerTestApp(t, repo)

		req := putSavedItemRequest(
			t,
			uuid.New(),
			uuid.New(),
			`{"collection_name": "Wishlist"}`,
			false,
		)
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

	t.Run("keeps the driver detail out of an internal error response", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		userID := uuid.New()
		savedItemID := uuid.New()

		repo.EXPECT().
			PutSavedItemIntoUserCollection(gomock.Any(), gomock.Any()).
			Return(
				collection.PutSavedItemIntoUserCollectionResult{},
				apperror.Internal(errors.New("connection reset by peer")),
			)

		app := newHandlerTestApp(t, repo)

		resp, err := app.Test(
			putSavedItemRequest(
				t,
				userID,
				savedItemID,
				`{"collection_name": "Wishlist"}`,
				true,
			),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		require.NotContains(
			t,
			readBody(t, resp),
			"connection reset by peer",
		)
	})
}
