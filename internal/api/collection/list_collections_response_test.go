package collection_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/collection"
	collectionmocks "github.com/thoriqr/stash-it-backend/internal/api/collection/mocks"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/validation"
)

// newCollectionListTestApp mounts the real collection list handler behind the real
// auth middleware, with only the repository substituted.
//
// It mounts CollectionRoutes rather than Routes because that is the group the
// collection listing is registered on. The repository is the only thing faked: the
// bounds in the count pair are decided by SQL and asserted against real rows in the
// integration tests, while what is asserted here is that whatever the statement
// produced reaches the wire unchanged and under the agreed key names.
func newCollectionListTestApp(
	t *testing.T,
	repo collection.Repository,
) *fiber.App {
	t.Helper()

	app := fiber.New(fiber.Config{
		ErrorHandler:    httpx.NewErrorHandler(zap.NewNop()),
		StructValidator: validation.New(),
	})

	collection.CollectionRoutes(
		app.Group("/collections"),
		collection.NewHandler(collection.NewService(repo)),
		security.NewAccessTokenVerifier([]byte(handlerTestAccessTokenSecret)),
	)

	return app
}

// listedCollectionPayload decodes the response the way a client would, so the test
// asserts on the wire shape rather than on the API response type it also compiles
// against.
type listedCollectionPayload struct {
	Data struct {
		Collections []struct {
			ID                      string    `json:"id"`
			Name                    string    `json:"name"`
			Type                    string    `json:"type"`
			SystemKey               *string   `json:"system_key"`
			SavedItemsCount         int64     `json:"saved_items_count"`
			SavedItemsCountOverflow bool      `json:"saved_items_count_overflow"`
			CreatedAt               time.Time `json:"created_at"`
			UpdatedAt               time.Time `json:"updated_at"`
		} `json:"collections"`
	} `json:"data"`
	Message string `json:"message"`
	Meta    struct {
		Cursor *struct {
			NextCursor *string `json:"next_cursor"`
			HasMore    bool    `json:"has_more"`
		} `json:"cursor"`
	} `json:"meta"`
}

// getCollectionsList calls GET /collections and requires a 200 with a decoded body.
func getCollectionsList(
	t *testing.T,
	app *fiber.App,
	userID uuid.UUID,
	query string,
) listedCollectionPayload {
	t.Helper()

	path := "/collections"
	if query != "" {
		path += "?" + query
	}

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(
		"Authorization",
		"Bearer "+handlerTestToken(t, userID),
	)

	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var page listedCollectionPayload

	require.NoError(t, json.Unmarshal(raw, &page))
	require.NotNil(t, page.Meta.Cursor)

	return page
}

// listedCollectionRowWithCount builds a listing row carrying a given count pair, the
// way the repository hands it to the mapper.
func listedCollectionRowWithCount(
	id uuid.UUID,
	name string,
	systemKey *string,
	count int64,
	overflow bool,
) collection.ListedCollection {
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	row := collection.ListedCollection{
		ID:                      id,
		UserID:                  uuid.New(),
		Name:                    name,
		Type:                    "user",
		CreatedAt:               pgtype.Timestamptz{Time: createdAt, Valid: true},
		UpdatedAt:               pgtype.Timestamptz{Time: createdAt, Valid: true},
		SavedItemsCount:         count,
		SavedItemsCountOverflow: overflow,
	}

	if systemKey != nil {
		row.Type = "system"
		row.SystemKey = pgtype.Text{String: *systemKey, Valid: true}
	}

	return row
}

// TestHandler_ListCollections_SavedItemsCountFields pins the wire contract of the
// two count fields.
//
// The point of the pair is that a client never has to know the threshold. These cases
// cover every state the product distinguishes: nothing held, an exact count below
// the bound, and a count above it. Reporting the cap without the flag would make
// "99 items" and "99 or more" the same response, and reporting the flag without
// always sending it would make a client work out whether the key was absent or false.
func TestHandler_ListCollections_SavedItemsCountFields(t *testing.T) {
	cases := []struct {
		name         string
		count        int64
		overflow     bool
		wantCount    int64
		wantOverflow bool
	}{
		{
			// An empty collection is an ordinary state in this product, so it is
			// reported as zero rather than as an absence.
			name:         "an empty collection reports zero",
			count:        0,
			overflow:     false,
			wantCount:    0,
			wantOverflow: false,
		},
		{
			name:         "one item reports one",
			count:        1,
			overflow:     false,
			wantCount:    1,
			wantOverflow: false,
		},
		{
			name:         "a count at the bound reports exactly",
			count:        collection.CollectionSavedItemsCountCap,
			overflow:     false,
			wantCount:    collection.CollectionSavedItemsCountCap,
			wantOverflow: false,
		},
		{
			name:         "a count above the bound reports the cap and the flag",
			count:        collection.CollectionSavedItemsCountCap,
			overflow:     true,
			wantCount:    collection.CollectionSavedItemsCountCap,
			wantOverflow: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := collectionmocks.NewMockRepository(gomock.NewController(t))

			collectionID := uuid.New()

			repo.EXPECT().
				ListCollections(gomock.Any(), gomock.Any()).
				Return(collection.ListCollectionsResult{
					Collections: []collection.ListedCollection{
						listedCollectionRowWithCount(
							collectionID,
							"Wishlist",
							nil,
							tc.count,
							tc.overflow,
						),
					},
					Limit: collection.CollectionListDefaultLimit,
					Sort:  collection.CollectionSortNewest,
				}, nil)

			page := getCollectionsList(
				t,
				newCollectionListTestApp(t, repo),
				uuid.New(),
				"",
			)

			require.Len(t, page.Data.Collections, 1)
			require.Equal(t, collectionID.String(), page.Data.Collections[0].ID)
			require.Equal(t, tc.wantCount, page.Data.Collections[0].SavedItemsCount)
			require.Equal(
				t,
				tc.wantOverflow,
				page.Data.Collections[0].SavedItemsCountOverflow,
			)
		})
	}

	// Both keys are always present, including where they carry their zero value.
	// A client that has to distinguish a missing key from false has to branch, and a
	// JSON field dropped by omitempty is exactly how that arrives.
	t.Run("both count keys are always present", func(t *testing.T) {
		repo := collectionmocks.NewMockRepository(gomock.NewController(t))

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			Return(collection.ListCollectionsResult{
				Collections: []collection.ListedCollection{
					listedCollectionRowWithCount(
						uuid.New(),
						"Empty",
						nil,
						0,
						false,
					),
				},
				Limit: collection.CollectionListDefaultLimit,
				Sort:  collection.CollectionSortNewest,
			}, nil)

		app := newCollectionListTestApp(t, repo)

		req := httptest.NewRequest(http.MethodGet, "/collections", nil)
		req.Header.Set(
			"Authorization",
			"Bearer "+handlerTestToken(t, uuid.New()),
		)

		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var body map[string]any

		require.NoError(t, json.Unmarshal(raw, &body))

		data, ok := body["data"].(map[string]any)
		require.True(t, ok, "the response must carry a data object")

		collections, ok := data["collections"].([]any)
		require.True(t, ok, "data must carry a collections array")
		require.Len(t, collections, 1)

		entry, ok := collections[0].(map[string]any)
		require.True(t, ok, "each collection must be an object")

		require.Contains(t, entry, "saved_items_count")
		require.Contains(t, entry, "saved_items_count_overflow")

		// Zero must survive as a number, not as null and not as an absent key.
		require.Equal(
			t,
			float64(0),
			entry["saved_items_count"],
			"an empty collection must report 0, not null",
		)
		require.Equal(
			t,
			false,
			entry["saved_items_count_overflow"],
			"the overflow flag must be false, not absent",
		)
	})

	// An empty listing is an empty array rather than null, which is the same rule
	// every list endpoint in this project follows.
	t.Run("an empty listing serializes as an empty array", func(t *testing.T) {
		repo := collectionmocks.NewMockRepository(gomock.NewController(t))

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			Return(collection.ListCollectionsResult{
				Collections: []collection.ListedCollection{},
				Limit:       collection.CollectionListDefaultLimit,
				Sort:        collection.CollectionSortNewest,
			}, nil)

		page := getCollectionsList(
			t,
			newCollectionListTestApp(t, repo),
			uuid.New(),
			"",
		)

		require.NotNil(t, page.Data.Collections)
		require.Empty(t, page.Data.Collections)
	})
}
