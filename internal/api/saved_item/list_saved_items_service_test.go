package saved_item_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	saveditemmocks "github.com/thoriqr/stash-it-backend/internal/api/saved_item/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/pagination"
)

func newRecentItemsService(
	t *testing.T,
) (saved_item.SavedItemService, *saveditemmocks.MockRepository) {
	t.Helper()

	repo := saveditemmocks.NewMockRepository(gomock.NewController(t))

	return saved_item.NewService(repo, nil, zap.NewNop()), repo
}

// recentSavedItemRow builds one row of the recent listing.
//
// createdAt is passed explicitly rather than defaulted, because these tests are
// about positions in an ordering and a row with an arbitrary timestamp could not
// assert one.
func recentSavedItemRow(
	id uuid.UUID,
	createdAt time.Time,
	collectionID uuid.UUID,
	collectionName string,
) saved_item.ListedSavedItem {
	return saved_item.ListedSavedItem{
		ID:               id,
		Url:              "https://example.com/a",
		CollectionID:     collectionID,
		CollectionName:   collectionName,
		EnrichmentStatus: "pending",
		CreatedAt:        pgtype.Timestamptz{Time: createdAt, Valid: true},
		UpdatedAt:        pgtype.Timestamptz{Time: createdAt, Valid: true},
	}
}

// recentItemsCursorToken encodes a recent-items cursor the way a client would, so a
// test builds a token the service cannot have produced itself.
func recentItemsCursorToken(
	t *testing.T,
	c saved_item.ListSavedItemsCursor,
) string {
	t.Helper()

	if c.Version == 0 {
		c.Version = pagination.CurrentVersion
	}

	token, err := pagination.Encode(c)
	require.NoError(t, err)

	return token
}

// decodeRecentItemsToken reads a cursor the service issued, so a test asserts on what
// the service encoded rather than on the opaque string.
func decodeRecentItemsToken(
	t *testing.T,
	token string,
) saved_item.ListSavedItemsCursor {
	t.Helper()

	decoded, err := pagination.Decode[saved_item.ListSavedItemsCursor](token)
	require.NoError(t, err)

	return decoded
}

// assertRecentItemsCursorRejected asserts that a token was refused with the single
// 400 every unusable cursor produces.
//
// One code and one status for all of them is deliberate: a cursor is not a
// capability, so "malformed" and "unusable" mean one thing to a caller, which is to
// start over without a cursor. Distinguishing them in the message would only make
// the token's internal shape easier to probe.
func assertRecentItemsCursorRejected(t *testing.T, err error) {
	t.Helper()

	require.Error(t, err)
	require.Equal(
		t,
		saved_item.CodeInvalidCursor,
		apperror.FromError(err).Code,
	)
	require.Equal(t, http.StatusBadRequest, apperror.FromError(err).Status)
}

func TestService_List(t *testing.T) {
	t.Run("defaults an absent limit", func(t *testing.T) {
		svc, repo := newRecentItemsService(t)

		repo.EXPECT().
			ListSavedItems(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params saved_item.ListSavedItemsParams,
				) (saved_item.ListSavedItemsResult, error) {
					require.Equal(
						t,
						saved_item.SavedItemListDefaultLimit,
						params.Limit,
					)

					return saved_item.ListSavedItemsResult{
						SavedItems: []saved_item.ListedSavedItem{},
						Limit:      params.Limit,
					}, nil
				},
			)

		result, err := svc.List(context.Background(), uuid.New(), 0, "")

		require.NoError(t, err)
		require.Equal(t, saved_item.SavedItemListDefaultLimit, result.Limit)
	})

	// A limit of zero reaches this service from a caller that read the query
	// parameter as absent, so it has to mean the default rather than a zero-row page.
	t.Run("reads a zero limit as absent", func(t *testing.T) {
		svc, repo := newRecentItemsService(t)

		repo.EXPECT().
			ListSavedItems(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params saved_item.ListSavedItemsParams,
				) (saved_item.ListSavedItemsResult, error) {
					require.Equal(
						t,
						saved_item.SavedItemListDefaultLimit,
						params.Limit,
					)

					return saved_item.ListSavedItemsResult{
						SavedItems: []saved_item.ListedSavedItem{},
						Limit:      params.Limit,
					}, nil
				},
			)

		_, err := svc.List(context.Background(), uuid.New(), 0, "")

		require.NoError(t, err)
	})

	// The clamp exists for callers that reach the service without the request type's
	// max=50 tag firing. The tag is the primary guard; this is the backstop.
	t.Run("clamps a limit above the maximum", func(t *testing.T) {
		svc, repo := newRecentItemsService(t)

		repo.EXPECT().
			ListSavedItems(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params saved_item.ListSavedItemsParams,
				) (saved_item.ListSavedItemsResult, error) {
					require.Equal(
						t,
						saved_item.SavedItemListMaxLimit,
						params.Limit,
					)

					return saved_item.ListSavedItemsResult{
						SavedItems: []saved_item.ListedSavedItem{},
						Limit:      params.Limit,
					}, nil
				},
			)

		_, err := svc.List(
			context.Background(),
			uuid.New(),
			saved_item.SavedItemListMaxLimit+10,
			"",
		)

		require.NoError(t, err)
	})

	// The listing is scoped to the authenticated user, and the id comes from the
	// token rather than from anywhere in the request.
	t.Run("scopes the listing to the authenticated user", func(t *testing.T) {
		svc, repo := newRecentItemsService(t)

		userID := uuid.New()

		repo.EXPECT().
			ListSavedItems(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params saved_item.ListSavedItemsParams,
				) (saved_item.ListSavedItemsResult, error) {
					require.Equal(t, userID, params.UserID)
					require.Nil(t, params.Cursor)

					return saved_item.ListSavedItemsResult{
						SavedItems: []saved_item.ListedSavedItem{},
						Limit:      params.Limit,
					}, nil
				},
			)

		_, err := svc.List(context.Background(), userID, 20, "")

		require.NoError(t, err)
	})

	// The listing reads and does nothing else: no enqueuer is consulted, no write
	// happens and nothing is scheduled. The service holds no enqueuer for this path
	// at all, and this asserts the observable part of that, which is that one
	// repository call is the whole of it.
	t.Run("delegates to the repository and does nothing else", func(t *testing.T) {
		svc, repo := newRecentItemsService(t)

		calls := 0

		repo.EXPECT().
			ListSavedItems(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					context.Context,
					saved_item.ListSavedItemsParams,
				) (saved_item.ListSavedItemsResult, error) {
					calls++

					return saved_item.ListSavedItemsResult{}, nil
				},
			).
			Times(1)

		_, err := svc.List(context.Background(), uuid.New(), 20, "")

		require.NoError(t, err)
		require.Equal(t, 1, calls)
	})

	t.Run("returns repository errors unchanged", func(t *testing.T) {
		svc, repo := newRecentItemsService(t)

		repoErr := apperror.Internal(errors.New("listing failed"))

		repo.EXPECT().
			ListSavedItems(gomock.Any(), gomock.Any()).
			Return(saved_item.ListSavedItemsResult{}, repoErr)

		_, err := svc.List(context.Background(), uuid.New(), 20, "")

		require.Equal(t, repoErr, err)
	})
}

func TestService_List_CursorValidation(t *testing.T) {
	validValue := "2026-10-02T10:30:00Z"
	validID := uuid.New()

	// Each case below declares Times(0) on the repository call, so a service that
	// spent a query discovering the token was unusable fails the test through gomock
	// strict mode. That is the property being asserted: a bad token costs no
	// connection, and an unusable position can never become a server fault.
	rejected := []struct {
		name  string
		token string
	}{
		{
			name:  "a token that is not base64",
			token: "not a cursor!!",
		},
		{
			name:  "a token that is not json",
			token: base64.RawURLEncoding.EncodeToString([]byte("not json")),
		},
		{
			name:  "a payload of the wrong shape",
			token: base64.RawURLEncoding.EncodeToString([]byte(`["array"]`)),
		},
		{
			name: "an unsupported payload version",
			token: recentItemsCursorToken(t, saved_item.ListSavedItemsCursor{
				Version: pagination.CurrentVersion + 1,
				Value:   &validValue,
				ID:      &validID,
			}),
		},
		{
			name: "no position at all",
			token: recentItemsCursorToken(
				t,
				saved_item.ListSavedItemsCursor{Version: pagination.CurrentVersion},
			),
		},
		{
			// An ordering on created_at alone is not total, so a position without the
			// tie-breaker cannot say where a page ended. Reading it as the top of the
			// history would silently hand a caller the newest page.
			name: "a value with no tie-breaking id",
			token: recentItemsCursorToken(t, saved_item.ListSavedItemsCursor{
				Version: pagination.CurrentVersion,
				Value:   &validValue,
			}),
		},
		{
			name: "an id with no value",
			token: recentItemsCursorToken(t, saved_item.ListSavedItemsCursor{
				Version: pagination.CurrentVersion,
				ID:      &validID,
			}),
		},
		{
			name: "an empty value",
			token: recentItemsCursorToken(t, saved_item.ListSavedItemsCursor{
				Version: pagination.CurrentVersion,
				Value:   ptrTo(""),
				ID:      &validID,
			}),
		},
		{
			name: "the nil id",
			token: recentItemsCursorToken(t, saved_item.ListSavedItemsCursor{
				Version: pagination.CurrentVersion,
				Value:   &validValue,
				ID:      &uuid.Nil,
			}),
		},
		{
			// The position is compared against a timestamptz column, so a string that
			// is not a timestamp has to be refused here. Letting it reach the query
			// would turn a client's bad token into a server fault.
			name: "a value that is not a timestamp",
			token: recentItemsCursorToken(t, saved_item.ListSavedItemsCursor{
				Version: pagination.CurrentVersion,
				Value:   ptrTo("not-a-timestamp"),
				ID:      &validID,
			}),
		},
		{
			name: "a value that looks like a timestamp but is not one",
			token: recentItemsCursorToken(t, saved_item.ListSavedItemsCursor{
				Version: pagination.CurrentVersion,
				Value:   ptrTo("1750000000"),
				ID:      &validID,
			}),
		},
	}

	for _, tc := range rejected {
		t.Run("rejects "+tc.name+" before any query runs", func(t *testing.T) {
			svc, repo := newRecentItemsService(t)

			repo.EXPECT().ListSavedItems(gomock.Any(), gomock.Any()).Times(0)

			_, err := svc.List(context.Background(), uuid.New(), 20, tc.token)

			assertRecentItemsCursorRejected(t, err)
		})
	}

	// The repository must receive the type sqlc generated for a timestamptz column,
	// so nothing has to be reinterpreted after it has been validated.
	t.Run("hands the repository a parsed timestamp parameter", func(t *testing.T) {
		svc, repo := newRecentItemsService(t)

		repo.EXPECT().
			ListSavedItems(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params saved_item.ListSavedItemsParams,
				) (saved_item.ListSavedItemsResult, error) {
					require.NotNil(t, params.Cursor)

					timestamp := params.Cursor.Timestamp

					require.True(t, timestamp.Valid, "the parameter must be usable")

					expected, parseErr := time.Parse(
						time.RFC3339Nano,
						validValue,
					)
					require.NoError(t, parseErr)
					require.True(t, expected.Equal(timestamp.Time))

					return saved_item.ListSavedItemsResult{}, nil
				},
			)

		_, err := svc.List(
			context.Background(),
			uuid.New(),
			20,
			recentItemsCursorToken(t, saved_item.ListSavedItemsCursor{
				Version: pagination.CurrentVersion,
				Value:   &validValue,
				ID:      &validID,
			}),
		)

		require.NoError(t, err)
	})

	t.Run("accepts a valid cursor and passes the position on", func(t *testing.T) {
		svc, repo := newRecentItemsService(t)

		repo.EXPECT().
			ListSavedItems(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params saved_item.ListSavedItemsParams,
				) (saved_item.ListSavedItemsResult, error) {
					require.NotNil(t, params.Cursor)
					require.NotNil(t, params.Cursor.Value)
					require.NotNil(t, params.Cursor.ID)
					require.Equal(t, validID, *params.Cursor.ID)

					return saved_item.ListSavedItemsResult{}, nil
				},
			)

		_, err := svc.List(
			context.Background(),
			uuid.New(),
			20,
			recentItemsCursorToken(t, saved_item.ListSavedItemsCursor{
				Version: pagination.CurrentVersion,
				Value:   &validValue,
				ID:      &validID,
			}),
		)

		require.NoError(t, err)
	})
}

func TestService_List_NextCursor(t *testing.T) {
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	// The cursor must be built from the last row the caller received, not from the
	// lookahead row. Getting this wrong skips one item at every page boundary, which
	// is silent and would not fail any single-page test.
	t.Run("is built from the last returned row, not the lookahead", func(t *testing.T) {
		svc, repo := newRecentItemsService(t)

		collectionID := uuid.New()

		first := recentSavedItemRow(uuid.New(), createdAt, collectionID, "Unsorted")
		last := recentSavedItemRow(uuid.New(), createdAt, collectionID, "Unsorted")

		// The repository already dropped the lookahead row, so it returns more than
		// the limit and HasMore is set. The cursor must describe last.
		repo.EXPECT().
			ListSavedItems(gomock.Any(), gomock.Any()).
			Return(saved_item.ListSavedItemsResult{
				SavedItems: []saved_item.ListedSavedItem{first, last},
				Limit:      2,
				HasMore:    true,
			}, nil)

		result, err := svc.List(context.Background(), uuid.New(), 2, "")

		require.NoError(t, err)
		require.True(t, result.HasMore)
		require.NotNil(t, result.NextCursor)

		cursor := decodeRecentItemsToken(t, *result.NextCursor)

		require.NotNil(t, cursor.ID)
		require.Equal(
			t,
			last.ID,
			*cursor.ID,
			"the cursor must point at the last row returned",
		)
	})

	// An empty page has no last row to point at, so there is nothing to encode.
	// It is not an error and it is not a page with more to come.
	t.Run("is absent on an empty page", func(t *testing.T) {
		svc, repo := newRecentItemsService(t)

		repo.EXPECT().
			ListSavedItems(gomock.Any(), gomock.Any()).
			Return(saved_item.ListSavedItemsResult{
				SavedItems: []saved_item.ListedSavedItem{},
				Limit:      20,
				HasMore:    true,
			}, nil)

		result, err := svc.List(context.Background(), uuid.New(), 20, "")

		require.NoError(t, err)
		require.False(t, result.HasMore)
		require.Nil(t, result.NextCursor)
	})

	t.Run("is absent on the final page", func(t *testing.T) {
		svc, repo := newRecentItemsService(t)

		collectionID := uuid.New()

		repo.EXPECT().
			ListSavedItems(gomock.Any(), gomock.Any()).
			Return(saved_item.ListSavedItemsResult{
				SavedItems: []saved_item.ListedSavedItem{
					recentSavedItemRow(uuid.New(), createdAt, collectionID, "Unsorted"),
				},
				Limit:   20,
				HasMore: false,
			}, nil)

		result, err := svc.List(context.Background(), uuid.New(), 20, "")

		require.NoError(t, err)
		require.False(t, result.HasMore)
		require.Nil(t, result.NextCursor)
	})
}

// TestListSavedItemsCursor_RoundTripThroughJSON checks the payload survives a real
// JSON round trip with the exact key names the wire format uses, since the field
// names are the contract with any client that stores a cursor.
func TestListSavedItemsCursor_RoundTripThroughJSON(t *testing.T) {
	value := "2026-10-02T10:30:00Z"
	id := uuid.New()

	raw, err := json.Marshal(saved_item.ListSavedItemsCursor{
		Version: pagination.CurrentVersion,
		Value:   &value,
		ID:      &id,
	})
	require.NoError(t, err)

	var decoded saved_item.ListSavedItemsCursor

	require.NoError(t, json.Unmarshal(raw, &decoded))

	require.Equal(t, pagination.CurrentVersion, decoded.Version)
	require.NotNil(t, decoded.Value)
	require.Equal(t, value, *decoded.Value)
	require.NotNil(t, decoded.ID)
	require.Equal(t, id, *decoded.ID)

	// Timestamp is derived, not transmitted. It must not appear in the token at all,
	// or the payload would carry a second copy of the position that could disagree
	// with the one it was encoded from.
	require.NotContains(t, string(raw), "Timestamp")
}

// TestListSavedItemsCursor_CarriesNoSortOrGroup pins the payload to the smallest
// shape this listing can justify.
//
// This listing has one ordering and no pinned row, so a token cannot be mismatched
// against a sort and there is no group to record. Carrying either would mean every
// validation branch for them could never be reached, and a token carrying a sort
// would be claiming something that does not exist here. The collection listing's
// payload carries both for the opposite reason, and the contrast is the point.
func TestListSavedItemsCursor_CarriesNoSortOrGroup(t *testing.T) {
	raw, err := json.Marshal(saved_item.ListSavedItemsCursor{
		Version: pagination.CurrentVersion,
		Value:   ptrTo("2026-10-02T10:30:00Z"),
		ID:      ptrTo(uuid.New()),
	})
	require.NoError(t, err)

	var fields map[string]any

	require.NoError(t, json.Unmarshal(raw, &fields))

	require.Contains(t, fields, "v")
	require.Contains(t, fields, "val")
	require.Contains(t, fields, "i")
	require.NotContains(t, fields, "s", "this listing has one ordering")
	require.NotContains(t, fields, "g", "this listing has no pinned row")
}

func ptrTo[T any](value T) *T {
	return &value
}
