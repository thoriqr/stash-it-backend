package collection_test

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

	"github.com/thoriqr/stash-it-backend/internal/api/collection"
	collectiondb "github.com/thoriqr/stash-it-backend/internal/api/collection/generated"
	collectionmocks "github.com/thoriqr/stash-it-backend/internal/api/collection/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/pagination"
)

func newListService(
	t *testing.T,
) (collection.Service, *collectionmocks.MockRepository) {
	t.Helper()

	repo := collectionmocks.NewMockRepository(gomock.NewController(t))

	return collection.NewService(repo), repo
}

func listCollectionRow(
	id uuid.UUID,
	name string,
	createdAt time.Time,
	systemKey *string,
) collectiondb.Collection {
	row := collectiondb.Collection{
		ID:        id,
		Name:      name,
		Type:      "user",
		CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
	}

	if systemKey != nil {
		row.Type = "system"
		row.SystemKey = pgtype.Text{String: *systemKey, Valid: true}
	}

	return row
}

func unsortedKey() *string {
	key := string(collection.CollectionSystemKeyUnsorted)

	return &key
}

// decodeToken reads a cursor the service issued, so a test asserts on what the
// service encoded rather than on the opaque string.
func decodeToken(
	t *testing.T,
	token string,
) collection.ListCollectionsCursor {
	t.Helper()

	decoded, err := pagination.Decode[collection.ListCollectionsCursor](token)
	require.NoError(t, err)

	return decoded
}

func TestService_ListCollections(t *testing.T) {
	t.Run("defaults to newest first", func(t *testing.T) {
		svc, repo := newListService(t)

		userID := uuid.New()

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params collection.ListCollectionsParams,
				) (collection.ListCollectionsResult, error) {
					require.Equal(
						t,
						collection.CollectionSortNewest,
						params.Sort,
					)
					require.Nil(t, params.Cursor)

					return collection.ListCollectionsResult{}, nil
				},
			)

		result, err := svc.ListCollections(
			context.Background(),
			userID,
			"",
			0,
			"",
		)

		require.NoError(t, err)
		require.Equal(t, collection.CollectionSortNewest, result.Sort)
	})

	t.Run("uses the default limit when none is given", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params collection.ListCollectionsParams,
				) (collection.ListCollectionsResult, error) {
					require.Equal(
						t,
						collection.CollectionListDefaultLimit,
						params.Limit,
					)

					return collection.ListCollectionsResult{}, nil
				},
			)

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			"",
		)

		require.NoError(t, err)
	})

	t.Run("clamps a limit above the maximum", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params collection.ListCollectionsParams,
				) (collection.ListCollectionsResult, error) {
					require.Equal(
						t,
						collection.CollectionListMaxLimit,
						params.Limit,
					)

					return collection.ListCollectionsResult{}, nil
				},
			)

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			collection.CollectionListMaxLimit+10,
			"",
		)

		require.NoError(t, err)
	})

	// An unknown order must be rejected rather than passed to a query, because
	// choosing an order on the caller's behalf returns rows in a sequence they did
	// not ask for.
	t.Run("rejects an unsupported sort", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			Times(0)

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSort("random"),
			0,
			"",
		)

		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeInvalidCollectionSort,
			apperror.FromError(err).Code,
		)
		require.Equal(t, http.StatusBadRequest, apperror.FromError(err).Status)
	})

	for _, sort := range []collection.CollectionSort{
		collection.CollectionSortNewest,
		collection.CollectionSortOldest,
		collection.CollectionSortName,
	} {
		t.Run("forwards the "+string(sort)+" sort", func(t *testing.T) {
			svc, repo := newListService(t)

			repo.EXPECT().
				ListCollections(gomock.Any(), gomock.Any()).
				DoAndReturn(
					func(
						_ context.Context,
						params collection.ListCollectionsParams,
					) (collection.ListCollectionsResult, error) {
						require.Equal(t, sort, params.Sort)

						return collection.ListCollectionsResult{}, nil
					},
				)

			_, err := svc.ListCollections(
				context.Background(),
				uuid.New(),
				sort,
				0,
				"",
			)

			require.NoError(t, err)
		})
	}

	t.Run("passes the user id through unchanged", func(t *testing.T) {
		svc, repo := newListService(t)

		userID := uuid.New()

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params collection.ListCollectionsParams,
				) (collection.ListCollectionsResult, error) {
					require.Equal(t, userID, params.UserID)

					return collection.ListCollectionsResult{}, nil
				},
			)

		_, err := svc.ListCollections(
			context.Background(),
			userID,
			collection.CollectionSortNewest,
			0,
			"",
		)

		require.NoError(t, err)
	})
}

func TestService_ListCollections_NextCursor(t *testing.T) {
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	// The cursor must be built from the last row the caller received, not from the
	// lookahead row. Getting this wrong skips one collection at every page boundary,
	// which is silent and would not fail any single-page test.
	t.Run("is built from the last returned row, not the lookahead", func(t *testing.T) {
		svc, repo := newListService(t)

		first := listCollectionRow(uuid.New(), "First", createdAt, nil)
		last := listCollectionRow(uuid.New(), "Last", createdAt, nil)

		// The repository already dropped the lookahead row, so it returns more than
		// the limit and HasMore is set. The cursor must describe Last.
		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			Return(collection.ListCollectionsResult{
				Collections: []collectiondb.Collection{first, last},
				HasMore:     true,
			}, nil)

		result, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			2,
			"",
		)

		require.NoError(t, err)
		require.True(t, result.HasMore)
		require.NotNil(t, result.NextCursor)

		cursor := decodeToken(t, *result.NextCursor)

		require.NotNil(t, cursor.ID)
		require.Equal(
			t,
			last.ID,
			*cursor.ID,
			"the cursor must point at the last row returned",
		)
	})

	// Ending on Unsorted is the one position that needs no values: the next page
	// starts at the top of the regular ordering.
	t.Run("is group only when the page ended on unsorted", func(t *testing.T) {
		svc, repo := newListService(t)

		unsorted := listCollectionRow(
			uuid.New(),
			"Unsorted",
			createdAt,
			unsortedKey(),
		)

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			Return(collection.ListCollectionsResult{
				Collections: []collectiondb.Collection{unsorted},
				HasMore:     true,
			}, nil)

		result, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			1,
			"",
		)

		require.NoError(t, err)

		cursor := decodeToken(t, *result.NextCursor)

		require.NotNil(t, cursor.Group)
		require.Equal(t, 0, *cursor.Group)
		require.Nil(t, cursor.Value)
		require.Nil(t, cursor.ID)
	})

	// The name sort stores the normalized name, because the next query compares it
	// against lower(btrim(name)). A raw display name would compare against a value
	// the unique index never produced and quietly return the wrong rows.
	t.Run("stores the normalized name for the name sort", func(t *testing.T) {
		svc, repo := newListService(t)

		row := listCollectionRow(uuid.New(), "  WishList  ", createdAt, nil)

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			Return(collection.ListCollectionsResult{
				Collections: []collectiondb.Collection{row},
				HasMore:     true,
			}, nil)

		result, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortName,
			1,
			"",
		)

		require.NoError(t, err)

		cursor := decodeToken(t, *result.NextCursor)

		require.NotNil(t, cursor.Value)
		require.Equal(t, "wishlist", *cursor.Value)
	})

	// The time-based sorts store RFC3339Nano, which round-trips a timestamp exactly.
	t.Run("stores a round trippable timestamp for the time sorts", func(t *testing.T) {
		for _, sort := range []collection.CollectionSort{
			collection.CollectionSortNewest,
			collection.CollectionSortOldest,
		} {
			svc, repo := newListService(t)

			row := listCollectionRow(uuid.New(), "Anything", createdAt, nil)

			repo.EXPECT().
				ListCollections(gomock.Any(), gomock.Any()).
				Return(collection.ListCollectionsResult{
					Collections: []collectiondb.Collection{row},
					HasMore:     true,
				}, nil)

			result, err := svc.ListCollections(
				context.Background(),
				uuid.New(),
				sort,
				1,
				"",
			)

			require.NoError(t, err)

			cursor := decodeToken(t, *result.NextCursor)

			require.NotNil(t, cursor.Value)

			parsed, parseErr := time.Parse(time.RFC3339Nano, *cursor.Value)

			require.NoError(t, parseErr)
			require.True(t, parsed.Equal(createdAt))
		}
	})

	t.Run("records the sort it was issued for", func(t *testing.T) {
		svc, repo := newListService(t)

		row := listCollectionRow(uuid.New(), "Anything", createdAt, nil)

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			Return(collection.ListCollectionsResult{
				Collections: []collectiondb.Collection{row},
				HasMore:     true,
			}, nil)

		result, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortOldest,
			1,
			"",
		)

		require.NoError(t, err)

		cursor := decodeToken(t, *result.NextCursor)
		require.Equal(t, collection.CollectionSortOldest, cursor.Sort)
		require.Equal(t, pagination.CurrentVersion, cursor.Version)
	})

	// A final page and an empty page are the same shape. NextCursor nil is what the
	// response renders as an explicit null rather than omitting the field.
	t.Run("is nil when there are no more results", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			Return(collection.ListCollectionsResult{
				Collections: []collectiondb.Collection{
					listCollectionRow(uuid.New(), "Only", createdAt, nil),
				},
				HasMore: false,
			}, nil)

		result, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			1,
			"",
		)

		require.NoError(t, err)
		require.False(t, result.HasMore)
		require.Nil(t, result.NextCursor)
	})

	t.Run("is nil on an empty page", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			Return(collection.ListCollectionsResult{
				Collections: []collectiondb.Collection{},
				HasMore:     false,
			}, nil)

		result, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			1,
			"",
		)

		require.NoError(t, err)
		require.False(t, result.HasMore)
		require.Nil(t, result.NextCursor)
	})
}

func TestService_ListCollections_CursorValidation(t *testing.T) {
	validGroup := 1
	validValue := "2026-10-02T10:30:00Z"
	validID := uuid.New()

	cursorToken := func(t *testing.T, c collection.ListCollectionsCursor) string {
		t.Helper()

		if c.Version == 0 {
			c.Version = pagination.CurrentVersion
		}

		token, err := pagination.Encode(c)
		require.NoError(t, err)

		return token
	}

	newestAfterRow := func(t *testing.T) string {
		t.Helper()

		return cursorToken(t, collection.ListCollectionsCursor{
			Sort:  collection.CollectionSortNewest,
			Group: &validGroup,
			Value: &validValue,
			ID:    &validID,
		})
	}

	// Every rejection here is the same 400 with the same code. A cursor is not a
	// capability, so "malformed" and "wrong sort" mean one thing to a caller: start
	// over without a cursor.
	assertRejected := func(t *testing.T, err error) {
		t.Helper()

		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeInvalidCursor,
			apperror.FromError(err).Code,
		)
		require.Equal(t, http.StatusBadRequest, apperror.FromError(err).Status)
	}

	t.Run("rejects a token that is not base64", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().ListCollections(gomock.Any(), gomock.Any()).Times(0)

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			"not a cursor!!",
		)

		assertRejected(t, err)
	})

	t.Run("rejects a token that is not json", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().ListCollections(gomock.Any(), gomock.Any()).Times(0)

		token := base64.RawURLEncoding.EncodeToString([]byte("not json"))

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			token,
		)

		assertRejected(t, err)
	})

	t.Run("rejects a payload of the wrong shape", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().ListCollections(gomock.Any(), gomock.Any()).Times(0)

		token := base64.RawURLEncoding.EncodeToString([]byte(`["array"]`))

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			token,
		)

		assertRejected(t, err)
	})

	t.Run("rejects an unsupported version", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().ListCollections(gomock.Any(), gomock.Any()).Times(0)

		token := cursorToken(t, collection.ListCollectionsCursor{
			Version: pagination.CurrentVersion + 1,
			Sort:    collection.CollectionSortNewest,
			Group:   &validGroup,
			Value:   &validValue,
			ID:      &validID,
		})

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			token,
		)

		assertRejected(t, err)
	})

	// The bug a plain int Group would hide: an absent group decodes to 0, which is
	// exactly the after-Unsorted group. A truncated token would then read as valid and
	// silently skip the collection this product requires to appear first.
	t.Run("rejects a cursor with no group", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().ListCollections(gomock.Any(), gomock.Any()).Times(0)

		token := cursorToken(t, collection.ListCollectionsCursor{
			Sort: collection.CollectionSortNewest,
		})

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			token,
		)

		assertRejected(t, err)
	})

	t.Run("rejects an unknown group value", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().ListCollections(gomock.Any(), gomock.Any()).Times(0)

		group := 7

		token := cursorToken(t, collection.ListCollectionsCursor{
			Sort:  collection.CollectionSortNewest,
			Group: &group,
			Value: &validValue,
			ID:    &validID,
		})

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			token,
		)

		assertRejected(t, err)
	})

	t.Run("rejects an after-row cursor with no position", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().ListCollections(gomock.Any(), gomock.Any()).Times(0)

		token := cursorToken(t, collection.ListCollectionsCursor{
			Sort:  collection.CollectionSortNewest,
			Group: &validGroup,
		})

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			token,
		)

		assertRejected(t, err)
	})

	t.Run("rejects a zero uuid in the position", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().ListCollections(gomock.Any(), gomock.Any()).Times(0)

		nilID := uuid.Nil

		token := cursorToken(t, collection.ListCollectionsCursor{
			Sort:  collection.CollectionSortNewest,
			Group: &validGroup,
			Value: &validValue,
			ID:    &nilID,
		})

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			token,
		)

		assertRejected(t, err)
	})

	t.Run("rejects an empty sort value in the position", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().ListCollections(gomock.Any(), gomock.Any()).Times(0)

		empty := ""

		token := cursorToken(t, collection.ListCollectionsCursor{
			Sort:  collection.CollectionSortNewest,
			Group: &validGroup,
			Value: &empty,
			ID:    &validID,
		})

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			token,
		)

		assertRejected(t, err)
	})

	// This group means "the page ended on Unsorted", which carries no position. A
	// token claiming both is inconsistent rather than generous.
	t.Run("rejects an after-unsorted cursor carrying a position", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().ListCollections(gomock.Any(), gomock.Any()).Times(0)

		afterUnsorted := 0

		token := cursorToken(t, collection.ListCollectionsCursor{
			Sort:  collection.CollectionSortNewest,
			Group: &afterUnsorted,
			Value: &validValue,
			ID:    &validID,
		})

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			token,
		)

		assertRejected(t, err)
	})

	// A token that decodes cleanly and passes every structural check can still carry a
	// timestamp the time-based sorts cannot compare against a timestamptz column. It
	// has to be refused before any query runs: reaching the repository would make an
	// unusable token look like a server fault, and would spend a connection to
	// discover it.
	t.Run("rejects a non-empty but unparsable timestamp", func(t *testing.T) {
		for _, value := range []string{
			"not-a-timestamp",
			"2026-13-45T99:99:99Z",
			"1750000000",
			"2026-10-02 10:30:00",
			"z",
		} {
			svc, repo := newListService(t)

			repo.EXPECT().ListCollections(gomock.Any(), gomock.Any()).Times(0)

			badValue := value

			token := cursorToken(t, collection.ListCollectionsCursor{
				Sort:  collection.CollectionSortNewest,
				Group: &validGroup,
				Value: &badValue,
				ID:    &validID,
			})

			_, err := svc.ListCollections(
				context.Background(),
				uuid.New(),
				collection.CollectionSortNewest,
				0,
				token,
			)

			assertRejected(t, err)
		}
	})

	// The same rule under oldest, which is a different query and its own parameter
	// type in the generated code.
	t.Run("rejects an unparsable timestamp under oldest too", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().ListCollections(gomock.Any(), gomock.Any()).Times(0)

		badValue := "not-a-timestamp"
		oldestGroup := 1

		token := cursorToken(t, collection.ListCollectionsCursor{
			Sort:  collection.CollectionSortOldest,
			Group: &oldestGroup,
			Value: &badValue,
			ID:    &validID,
		})

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortOldest,
			0,
			token,
		)

		assertRejected(t, err)
	})

	// The name sort compares against text, so a value that is not a timestamp is a
	// legitimate position there and must not be caught by the timestamp check. Using a
	// real collection name proves the check is scoped to the sorts that need it.
	t.Run("accepts a collection name as a value under the name sort", func(t *testing.T) {
		svc, repo := newListService(t)

		name := "wishlist"

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params collection.ListCollectionsParams,
				) (collection.ListCollectionsResult, error) {
					require.NotNil(t, params.Cursor)
					require.NotNil(t, params.Cursor.Value)
					require.Equal(t, name, *params.Cursor.Value)

					// Nothing is parsed for this sort, so no timestamp is expected.
					require.False(t, params.Cursor.Timestamp.Valid)

					return collection.ListCollectionsResult{}, nil
				},
			)

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortName,
			0,
			cursorToken(t, collection.ListCollectionsCursor{
				Sort:  collection.CollectionSortName,
				Group: &validGroup,
				Value: &name,
				ID:    &validID,
			}),
		)

		require.NoError(t, err)
	})

	// The repository must receive the type sqlc generated for a timestamptz column,
	// so that no value has to be reinterpreted after it has been validated.
	t.Run("hands the repository a parsed timestamp parameter", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params collection.ListCollectionsParams,
				) (collection.ListCollectionsResult, error) {
					require.NotNil(t, params.Cursor)

					timestamp := params.Cursor.Timestamp

					require.True(t, timestamp.Valid, "the parameter must be usable")

					expected, parseErr := time.Parse(
						time.RFC3339Nano,
						validValue,
					)
					require.NoError(t, parseErr)
					require.True(t, expected.Equal(timestamp.Time))

					return collection.ListCollectionsResult{}, nil
				},
			)

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			newestAfterRow(t),
		)

		require.NoError(t, err)
	})

	t.Run("rejects an unsupported sort inside the cursor", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().ListCollections(gomock.Any(), gomock.Any()).Times(0)

		token := cursorToken(t, collection.ListCollectionsCursor{
			Sort:  collection.CollectionSort("random"),
			Group: &validGroup,
			Value: &validValue,
			ID:    &validID,
		})

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			token,
		)

		assertRejected(t, err)
	})

	// A position recorded under one ordering means nothing under another, so reusing
	// a token with a different sort is rejected rather than silently misread.
	t.Run("rejects a cursor issued for a different sort", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().ListCollections(gomock.Any(), gomock.Any()).Times(0)

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortOldest,
			0,
			newestAfterRow(t),
		)

		assertRejected(t, err)
	})

	t.Run("accepts a valid cursor and passes the position on", func(t *testing.T) {
		svc, repo := newListService(t)

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params collection.ListCollectionsParams,
				) (collection.ListCollectionsResult, error) {
					require.NotNil(t, params.Cursor)
					require.NotNil(t, params.Cursor.Value)
					require.NotNil(t, params.Cursor.ID)
					require.Equal(t, validID, *params.Cursor.ID)

					return collection.ListCollectionsResult{}, nil
				},
			)

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			newestAfterRow(t),
		)

		require.NoError(t, err)
	})

	// A cursor that ended on Unsorted has no position, so the repository is handed
	// one that says so rather than a fabricated value.
	t.Run("passes an after-unsorted cursor on with no position", func(t *testing.T) {
		svc, repo := newListService(t)

		afterUnsorted := 0

		token := cursorToken(t, collection.ListCollectionsCursor{
			Sort:  collection.CollectionSortNewest,
			Group: &afterUnsorted,
		})

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params collection.ListCollectionsParams,
				) (collection.ListCollectionsResult, error) {
					require.NotNil(t, params.Cursor)
					require.Nil(t, params.Cursor.Value)
					require.Nil(t, params.Cursor.ID)

					return collection.ListCollectionsResult{}, nil
				},
			)

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			token,
		)

		require.NoError(t, err)
	})

	t.Run("returns repository errors unchanged", func(t *testing.T) {
		svc, repo := newListService(t)

		repoErr := apperror.Internal(errors.New("list failed"))

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			Return(collection.ListCollectionsResult{}, repoErr)

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			"",
		)

		require.Equal(t, repoErr, err)
	})

	// The service owns the sort rules, the limit rules and the cursor rules, and
	// otherwise does nothing but forward one call.
	t.Run("delegates to the repository and does nothing else", func(t *testing.T) {
		svc, repo := newListService(t)

		calls := 0

		repo.EXPECT().
			ListCollections(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					context.Context,
					collection.ListCollectionsParams,
				) (collection.ListCollectionsResult, error) {
					calls++

					return collection.ListCollectionsResult{}, nil
				},
			).
			Times(1)

		_, err := svc.ListCollections(
			context.Background(),
			uuid.New(),
			collection.CollectionSortNewest,
			0,
			"",
		)

		require.NoError(t, err)
		require.Equal(t, 1, calls)
	})
}

// TestListCollectionsCursor_RoundTripThroughJSON checks the payload survives a real
// JSON round trip with the exact key names the wire format uses, since the field
// names are the contract with any client that stores a cursor.
func TestListCollectionsCursor_RoundTripThroughJSON(t *testing.T) {
	group := 1
	value := "value"
	id := uuid.New()

	raw, err := json.Marshal(collection.ListCollectionsCursor{
		Version: pagination.CurrentVersion,
		Sort:    collection.CollectionSortName,
		Group:   &group,
		Value:   &value,
		ID:      &id,
	})
	require.NoError(t, err)

	var decoded collection.ListCollectionsCursor

	require.NoError(t, json.Unmarshal(raw, &decoded))

	require.Equal(t, pagination.CurrentVersion, decoded.Version)
	require.Equal(t, collection.CollectionSortName, decoded.Sort)
	require.NotNil(t, decoded.Group)
	require.Equal(t, group, *decoded.Group)
	require.NotNil(t, decoded.Value)
	require.Equal(t, value, *decoded.Value)
	require.NotNil(t, decoded.ID)
	require.Equal(t, id, *decoded.ID)
}
