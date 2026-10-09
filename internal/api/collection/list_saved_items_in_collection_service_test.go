package collection_test

import (
	"context"
	"encoding/base64"
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

func newSavedItemListService(
	t *testing.T,
) (collection.Service, *collectionmocks.MockRepository) {
	t.Helper()

	repo := collectionmocks.NewMockRepository(gomock.NewController(t))

	return collection.NewService(repo), repo
}

// expectOwnedCollection is the lookup the service makes before listing anything. Every
// test needs it, and a test that wants to fail ownership just omits this expectation.
//
// It returns the row it stubbed, carrying a Name, because the service now carries that
// row through to the response. A test asserting on the id alone would still pass if the
// name were dropped somewhere in between.
func expectOwnedCollection(
	t *testing.T,
	repo *collectionmocks.MockRepository,
	userID uuid.UUID,
	collectionID uuid.UUID,
) collectiondb.Collection {
	t.Helper()

	owned := collectiondb.Collection{
		ID:     collectionID,
		UserID: userID,
		Name:   "Wishlist",
	}

	repo.EXPECT().
		GetCollectionByIDForUser(gomock.Any(), collection.GetCollectionByIDForUserParams{
			ID:     collectionID,
			UserID: userID,
		}).
		Return(owned, nil).
		Times(1)

	return owned
}

func expectNotFoundCollection(
	t *testing.T,
	repo *collectionmocks.MockRepository,
	userID uuid.UUID,
	collectionID uuid.UUID,
) {
	t.Helper()

	repo.EXPECT().
		GetCollectionByIDForUser(gomock.Any(), gomock.Any()).
		Return(
			collectiondb.Collection{},
			apperror.NotFoundWith(
				collection.CodeCollectionNotFound,
				"collection not found",
				errors.New("no rows"),
			),
		).
		Times(1)
}

func listedItem(
	id uuid.UUID,
	collectionID uuid.UUID,
	createdAt time.Time,
	title string,
	status string,
) collection.ListedSavedItem {
	item := collection.ListedSavedItem{
		ID:               id,
		UserID:           collectionID2(id),
		URL:              "https://example.com/" + id.String(),
		CollectionID:     collectionID,
		EnrichmentStatus: status,
		CreatedAt:        pgtype.Timestamptz{Time: createdAt, Valid: true},
		UpdatedAt:        pgtype.Timestamptz{Time: createdAt, Valid: true},
	}

	if title != "" {
		item.Title = pgtype.Text{String: title, Valid: true}
	}

	return item
}

// collectionID2 gives each listed item a distinct but unrelated owner value. The
// service never inspects it, and a unique value keeps two items from looking alike
// in a failure message.
func collectionID2(id uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(uuid.Nil, id[:])
}

func decodeSavedItemCursor(
	t *testing.T,
	token string,
) collection.ListSavedItemsInCollectionCursor {
	t.Helper()

	decoded, err := pagination.Decode[collection.ListSavedItemsInCollectionCursor](
		token,
	)
	require.NoError(t, err)

	return decoded
}

func TestService_ListSavedItemsInCollection(t *testing.T) {
	t.Run("lists the collection's items", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		collectionID := uuid.New()
		createdAt := time.Now().UTC().Truncate(time.Microsecond)

		expectOwnedCollection(t, repo, userID, collectionID)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params collection.ListSavedItemsInCollectionParams,
				) (collection.ListSavedItemsInCollectionResult, error) {
					require.Equal(t, userID, params.UserID)
					require.Equal(t, collectionID, params.CollectionID)
					require.Nil(t, params.Cursor)

					return collection.ListSavedItemsInCollectionResult{
						SavedItems: []collection.ListedSavedItem{
							listedItem(uuid.New(), collectionID, createdAt, "Title", "completed"),
						},
					}, nil
				},
			)

		result, err := svc.ListSavedItemsInCollection(
			context.Background(),
			userID,
			collectionID,
			0,
			"",
		)

		require.NoError(t, err)
		require.Len(t, result.SavedItems, 1)
		require.False(t, result.HasMore)
		require.Nil(t, result.NextCursor)
	})

	t.Run("uses the default limit when none is given", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		expectOwnedCollection(t, repo, userID, collectionID)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params collection.ListSavedItemsInCollectionParams,
				) (collection.ListSavedItemsInCollectionResult, error) {
					require.Equal(
						t,
						collection.SavedItemListDefaultLimit,
						params.Limit,
					)

					return collection.ListSavedItemsInCollectionResult{}, nil
				},
			)

		_, err := svc.ListSavedItemsInCollection(
			context.Background(),
			userID,
			collectionID,
			0,
			"",
		)

		require.NoError(t, err)
	})

	t.Run("clamps a limit above the maximum", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		expectOwnedCollection(t, repo, userID, collectionID)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params collection.ListSavedItemsInCollectionParams,
				) (collection.ListSavedItemsInCollectionResult, error) {
					require.Equal(
						t,
						collection.SavedItemListMaxLimit,
						params.Limit,
					)

					return collection.ListSavedItemsInCollectionResult{}, nil
				},
			)

		_, err := svc.ListSavedItemsInCollection(
			context.Background(),
			userID,
			collectionID,
			collection.SavedItemListMaxLimit+10,
			"",
		)

		require.NoError(t, err)
	})

	// Ownership is proved before anything is listed, so another user's collection
	// never reaches an item query.
	t.Run("refuses a collection that is not the caller's", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		expectNotFoundCollection(t, repo, userID, collectionID)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			Times(0)

		_, err := svc.ListSavedItemsInCollection(
			context.Background(),
			userID,
			collectionID,
			0,
			"",
		)

		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeCollectionNotFound,
			apperror.FromError(err).Code,
		)
		require.Equal(t, http.StatusNotFound, apperror.FromError(err).Status)
	})

	// A cursor that cannot be used is rejected before the ownership lookup, because a
	// request that is already refused has nothing to look up: there is no collection
	// this caller is being shown.
	t.Run("rejects an unusable cursor before looking up the collection", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		repo.EXPECT().
			GetCollectionByIDForUser(gomock.Any(), gomock.Any()).
			Times(0)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			Times(0)

		_, err := svc.ListSavedItemsInCollection(
			context.Background(),
			uuid.New(),
			uuid.New(),
			0,
			"not a cursor!!",
		)

		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeInvalidCursor,
			apperror.FromError(err).Code,
		)
		require.Equal(t, http.StatusBadRequest, apperror.FromError(err).Status)
	})

	t.Run("returns repository errors unchanged", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		collectionID := uuid.New()
		repoErr := apperror.Internal(errors.New("list failed"))

		expectOwnedCollection(t, repo, userID, collectionID)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			Return(collection.ListSavedItemsInCollectionResult{}, repoErr)

		_, err := svc.ListSavedItemsInCollection(
			context.Background(),
			userID,
			collectionID,
			0,
			"",
		)

		require.Equal(t, repoErr, err)
	})

	t.Run("delegates to the repository and does nothing else", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		expectOwnedCollection(t, repo, userID, collectionID)

		calls := 0

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					context.Context,
					collection.ListSavedItemsInCollectionParams,
				) (collection.ListSavedItemsInCollectionResult, error) {
					calls++

					return collection.ListSavedItemsInCollectionResult{}, nil
				},
			).
			Times(1)

		_, err := svc.ListSavedItemsInCollection(
			context.Background(),
			userID,
			collectionID,
			0,
			"",
		)

		require.NoError(t, err)
		require.Equal(t, 1, calls)
	})
}

func TestService_ListSavedItemsInCollection_NextCursor(t *testing.T) {
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	// The cursor must describe the last item the caller received. Building it from the
	// lookahead row would skip one item at every page boundary, and that failure is
	// silent: nothing fails, one item is simply never returned.
	t.Run("is built from the last returned item, not the lookahead", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		firstID := uuid.New()
		lastID := uuid.New()

		expectOwnedCollection(t, repo, userID, collectionID)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			Return(collection.ListSavedItemsInCollectionResult{
				SavedItems: []collection.ListedSavedItem{
					listedItem(firstID, collectionID, createdAt, "First", "completed"),
					listedItem(lastID, collectionID, createdAt, "Last", "completed"),
				},
				HasMore: true,
			}, nil)

		result, err := svc.ListSavedItemsInCollection(
			context.Background(),
			userID,
			collectionID,
			2,
			"",
		)

		require.NoError(t, err)
		require.True(t, result.HasMore)
		require.NotNil(t, result.NextCursor)

		cursor := decodeSavedItemCursor(t, *result.NextCursor)

		require.NotNil(t, cursor.ID)
		require.Equal(
			t,
			lastID,
			*cursor.ID,
			"the cursor must point at the last item returned",
		)
	})

	t.Run("stores a round trippable timestamp", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		expectOwnedCollection(t, repo, userID, collectionID)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			Return(collection.ListSavedItemsInCollectionResult{
				SavedItems: []collection.ListedSavedItem{
					listedItem(uuid.New(), collectionID, createdAt, "Only", "completed"),
				},
				HasMore: true,
			}, nil)

		result, err := svc.ListSavedItemsInCollection(
			context.Background(),
			userID,
			collectionID,
			1,
			"",
		)

		require.NoError(t, err)

		cursor := decodeSavedItemCursor(t, *result.NextCursor)

		require.NotNil(t, cursor.Value)
		require.Equal(t, pagination.CurrentVersion, cursor.Version)

		parsed, parseErr := time.Parse(time.RFC3339Nano, *cursor.Value)

		require.NoError(t, parseErr)
		require.True(t, parsed.Equal(createdAt))
	})

	t.Run("is nil on a final page", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		expectOwnedCollection(t, repo, userID, collectionID)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			Return(collection.ListSavedItemsInCollectionResult{
				SavedItems: []collection.ListedSavedItem{
					listedItem(uuid.New(), collectionID, createdAt, "Only", "completed"),
				},
				HasMore: false,
			}, nil)

		result, err := svc.ListSavedItemsInCollection(
			context.Background(),
			userID,
			collectionID,
			1,
			"",
		)

		require.NoError(t, err)
		require.False(t, result.HasMore)
		require.Nil(t, result.NextCursor)
	})

	t.Run("is nil on an empty page", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		expectOwnedCollection(t, repo, userID, collectionID)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			Return(collection.ListSavedItemsInCollectionResult{
				SavedItems: []collection.ListedSavedItem{},
			}, nil)

		result, err := svc.ListSavedItemsInCollection(
			context.Background(),
			userID,
			collectionID,
			1,
			"",
		)

		require.NoError(t, err)
		require.False(t, result.HasMore)
		require.Nil(t, result.NextCursor)
	})
}

func TestService_ListSavedItemsInCollection_Collection(t *testing.T) {
	// The response reports the collection it read from, so a caller that arrived with
	// only an id has something to render a heading from and something to confirm the
	// page was scoped to what it asked for.
	t.Run("carries the looked-up collection onto the result", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		owned := expectOwnedCollection(t, repo, userID, collectionID)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			Return(
				collection.ListSavedItemsInCollectionResult{
					SavedItems: []collection.ListedSavedItem{
						listedItem(uuid.New(), collectionID, time.Now(), "One", "completed"),
					},
				},
				nil,
			)

		result, err := svc.ListSavedItemsInCollection(
			context.Background(), userID, collectionID, 0, "",
		)

		require.NoError(t, err)
		require.Equal(t, owned.ID, result.Collection.ID)
		require.Equal(t, owned.Name, result.Collection.Name)
		require.Equal(t, collectionID, result.Collection.ID)
	})

	// The lookup already happened; reporting the collection must not add a second one.
	// Times(1) on the stub is what proves this, and a second lookup would fail the test
	// rather than quietly costing a round trip per request.
	t.Run("reuses the ownership lookup rather than reading again", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		expectOwnedCollection(t, repo, userID, collectionID)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			Return(collection.ListSavedItemsInCollectionResult{}, nil).
			Times(1)

		_, err := svc.ListSavedItemsInCollection(
			context.Background(), userID, collectionID, 0, "",
		)

		require.NoError(t, err)
	})

	// An empty collection is still a named collection. Reporting it only when the page
	// was non-empty would make "nothing in it" indistinguishable from "no match".
	t.Run("is present when the collection holds nothing", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		expectOwnedCollection(t, repo, userID, collectionID)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			Return(
				collection.ListSavedItemsInCollectionResult{
					SavedItems: []collection.ListedSavedItem{},
				},
				nil,
			)

		result, err := svc.ListSavedItemsInCollection(
			context.Background(), userID, collectionID, 0, "",
		)

		require.NoError(t, err)
		require.Empty(t, result.SavedItems)
		require.Equal(t, collectionID, result.Collection.ID)
		require.Equal(t, "Wishlist", result.Collection.Name)
	})

	// The collection is not present when the request failed, and there is nothing to
	// report then: the id the caller sent proved to belong to nobody.
	t.Run("is absent when the collection is not the caller's", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		expectNotFoundCollection(t, repo, uuid.New(), uuid.New())

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			Times(0)

		result, err := svc.ListSavedItemsInCollection(
			context.Background(), uuid.New(), uuid.New(), 0, "",
		)

		require.Error(t, err)
		require.Equal(t, uuid.Nil, result.Collection.ID)
		require.Empty(t, result.Collection.Name)
	})

	// Unsorted reaches the result the same way any other collection does. A test that
	// only covered a user collection would not notice a branch on system_key.
	t.Run("reports unsorted like any other collection", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		unsortedID := uuid.New()

		repo.EXPECT().
			GetCollectionByIDForUser(
				gomock.Any(),
				collection.GetCollectionByIDForUserParams{
					ID:     unsortedID,
					UserID: userID,
				},
			).
			Return(
				collectiondb.Collection{
					ID:        unsortedID,
					UserID:    userID,
					Name:      "Unsorted",
					Type:      "system",
					SystemKey: pgtype.Text{String: "unsorted", Valid: true},
				},
				nil,
			).
			Times(1)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			Return(
				collection.ListSavedItemsInCollectionResult{
					SavedItems: []collection.ListedSavedItem{
						listedItem(uuid.New(), unsortedID, time.Now(), "", "pending"),
					},
				},
				nil,
			)

		result, err := svc.ListSavedItemsInCollection(
			context.Background(), userID, unsortedID, 0, "",
		)

		require.NoError(t, err)
		require.Equal(t, unsortedID, result.Collection.ID)
		require.Equal(t, "Unsorted", result.Collection.Name)
	})
}

func TestService_ListSavedItemsInCollection_CursorValidation(t *testing.T) {
	validValue := "2026-10-02T10:30:00Z"
	validID := uuid.New()

	validCursor := func(t *testing.T) string {
		t.Helper()

		token, err := pagination.Encode(
			collection.ListSavedItemsInCollectionCursor{
				Version: pagination.CurrentVersion,
				Value:   &validValue,
				ID:      &validID,
			},
		)
		require.NoError(t, err)

		return token
	}

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
		svc, repo := newSavedItemListService(t)

		repo.EXPECT().
			GetCollectionByIDForUser(gomock.Any(), gomock.Any()).
			Times(0)

		_, err := svc.ListSavedItemsInCollection(
			context.Background(),
			uuid.New(),
			uuid.New(),
			0,
			"not a cursor!!",
		)

		assertRejected(t, err)
	})

	t.Run("rejects a token that is not json", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		repo.EXPECT().
			GetCollectionByIDForUser(gomock.Any(), gomock.Any()).
			Times(0)

		token := base64.RawURLEncoding.EncodeToString([]byte("not json"))

		_, err := svc.ListSavedItemsInCollection(
			context.Background(),
			uuid.New(),
			uuid.New(),
			0,
			token,
		)

		assertRejected(t, err)
	})

	t.Run("rejects a payload of the wrong shape", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		repo.EXPECT().
			GetCollectionByIDForUser(gomock.Any(), gomock.Any()).
			Times(0)

		token := base64.RawURLEncoding.EncodeToString([]byte(`["array"]`))

		_, err := svc.ListSavedItemsInCollection(
			context.Background(),
			uuid.New(),
			uuid.New(),
			0,
			token,
		)

		assertRejected(t, err)
	})

	t.Run("rejects an unsupported version", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		repo.EXPECT().
			GetCollectionByIDForUser(gomock.Any(), gomock.Any()).
			Times(0)

		token, err := pagination.Encode(
			collection.ListSavedItemsInCollectionCursor{
				Version: pagination.CurrentVersion + 1,
				Value:   &validValue,
				ID:      &validID,
			},
		)
		require.NoError(t, err)

		_, err = svc.ListSavedItemsInCollection(
			context.Background(),
			uuid.New(),
			uuid.New(),
			0,
			token,
		)

		assertRejected(t, err)
	})

	// Both halves of the position are required. created_at alone is not a total order,
	// so a position without the tie-breaker could not say where a page ended.
	t.Run("rejects a cursor with no id", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		repo.EXPECT().
			GetCollectionByIDForUser(gomock.Any(), gomock.Any()).
			Times(0)

		token, err := pagination.Encode(
			collection.ListSavedItemsInCollectionCursor{
				Version: pagination.CurrentVersion,
				Value:   &validValue,
			},
		)
		require.NoError(t, err)

		_, err = svc.ListSavedItemsInCollection(
			context.Background(),
			uuid.New(),
			uuid.New(),
			0,
			token,
		)

		assertRejected(t, err)
	})

	t.Run("rejects a cursor with no value", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		repo.EXPECT().
			GetCollectionByIDForUser(gomock.Any(), gomock.Any()).
			Times(0)

		token, err := pagination.Encode(
			collection.ListSavedItemsInCollectionCursor{
				Version: pagination.CurrentVersion,
				ID:      &validID,
			},
		)
		require.NoError(t, err)

		_, err = svc.ListSavedItemsInCollection(
			context.Background(),
			uuid.New(),
			uuid.New(),
			0,
			token,
		)

		assertRejected(t, err)
	})

	t.Run("rejects a zero uuid in the position", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		repo.EXPECT().
			GetCollectionByIDForUser(gomock.Any(), gomock.Any()).
			Times(0)

		nilID := uuid.Nil

		token, err := pagination.Encode(
			collection.ListSavedItemsInCollectionCursor{
				Version: pagination.CurrentVersion,
				Value:   &validValue,
				ID:      &nilID,
			},
		)
		require.NoError(t, err)

		_, err = svc.ListSavedItemsInCollection(
			context.Background(),
			uuid.New(),
			uuid.New(),
			0,
			token,
		)

		assertRejected(t, err)
	})

	t.Run("rejects an empty value in the position", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		repo.EXPECT().
			GetCollectionByIDForUser(gomock.Any(), gomock.Any()).
			Times(0)

		empty := ""

		token, err := pagination.Encode(
			collection.ListSavedItemsInCollectionCursor{
				Version: pagination.CurrentVersion,
				Value:   &empty,
				ID:      &validID,
			},
		)
		require.NoError(t, err)

		_, err = svc.ListSavedItemsInCollection(
			context.Background(),
			uuid.New(),
			uuid.New(),
			0,
			token,
		)

		assertRejected(t, err)
	})

	// A token can decode cleanly and pass every structural check while still holding
	// something a timestamptz comparison cannot use. Reaching the repository would make
	// that look like a server fault and spend a connection to discover it.
	t.Run("rejects a non-empty but unparsable timestamp", func(t *testing.T) {
		for _, value := range []string{
			"not-a-timestamp",
			"2026-13-45T99:99:99Z",
			"1750000000",
			"z",
		} {
			svc, repo := newSavedItemListService(t)

			repo.EXPECT().
				GetCollectionByIDForUser(gomock.Any(), gomock.Any()).
				Times(0)

			repo.EXPECT().
				ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
				Times(0)

			badValue := value

			token, err := pagination.Encode(
				collection.ListSavedItemsInCollectionCursor{
					Version: pagination.CurrentVersion,
					Value:   &badValue,
					ID:      &validID,
				},
			)
			require.NoError(t, err)

			_, err = svc.ListSavedItemsInCollection(
				context.Background(),
				uuid.New(),
				uuid.New(),
				0,
				token,
			)

			assertRejected(t, err)
		}
	})

	// The repository must receive the type sqlc generated for a timestamptz column,
	// so no value is reinterpreted after it has been validated.
	t.Run("hands the repository a parsed timestamp parameter", func(t *testing.T) {
		svc, repo := newSavedItemListService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		expectOwnedCollection(t, repo, userID, collectionID)

		repo.EXPECT().
			ListSavedItemsInCollection(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params collection.ListSavedItemsInCollectionParams,
				) (collection.ListSavedItemsInCollectionResult, error) {
					require.NotNil(t, params.Cursor)

					timestamp := params.Cursor.Timestamp

					require.True(t, timestamp.Valid)

					expected, parseErr := time.Parse(
						time.RFC3339Nano,
						validValue,
					)
					require.NoError(t, parseErr)
					require.True(t, expected.Equal(timestamp.Time))

					return collection.ListSavedItemsInCollectionResult{}, nil
				},
			)

		_, err := svc.ListSavedItemsInCollection(
			context.Background(),
			userID,
			collectionID,
			0,
			validCursor(t),
		)

		require.NoError(t, err)
	})
}

// TestSavedItemCursorHasNoSortOrGroup asserts the payload carries only what this
// listing can use. A Sort or Group field here would be a field that can never vary,
// and every validation branch it invited would be unreachable.
func TestSavedItemCursorHasNoSortOrGroup(t *testing.T) {
	token, err := pagination.Encode(
		collection.ListSavedItemsInCollectionCursor{
			Version: pagination.CurrentVersion,
			Value:   strPtr("2026-10-02T10:30:00Z"),
			ID:      uuidPtr(uuid.New()),
		},
	)
	require.NoError(t, err)

	raw, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)

	require.NotContains(t, string(raw), `"s"`, "no sort may be carried")
	require.NotContains(t, string(raw), `"g"`, "no group may be carried")
}

func strPtr(value string) *string {
	return &value
}

func uuidPtr(value uuid.UUID) *uuid.UUID {
	return &value
}
