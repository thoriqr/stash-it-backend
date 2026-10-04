package collection_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/collection"
	collectiondb "github.com/thoriqr/stash-it-backend/internal/api/collection/generated"
	collectionmocks "github.com/thoriqr/stash-it-backend/internal/api/collection/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func TestService_PutSavedItem(t *testing.T) {
	t.Run("creates the collection and moves the saved item into it", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		svc := collection.NewService(repo)

		userID := uuid.New()
		savedItemID := uuid.New()
		targetID := uuid.New()

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
						ID:     targetID,
						UserID: userID,
						Name:   "Wishlist",
						Type:   "user",
					},
					CollectionCreated: true,
					SavedItem: collection.SavedItem{
						ID:           savedItemID,
						UserID:       userID,
						CollectionID: targetID,
					},
				},
				nil,
			)

		result, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"Wishlist",
		)

		require.NoError(t, err)
		require.True(t, result.CollectionCreated)
		require.False(t, result.AlreadyInCollection)
		require.Equal(t, targetID, result.Collection.ID)
		require.Equal(t, targetID, result.SavedItem.CollectionID)
	})

	t.Run("trims surrounding whitespace from the name", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		svc := collection.NewService(repo)

		userID := uuid.New()
		savedItemID := uuid.New()

		cases := map[string]string{
			"leading and trailing spaces": "   Wishlist   ",
			"leading tab":                 "\tWishlist",
			"trailing newline":            "Wishlist\n",
			"all unicode whitespace":      " Wishlist ",
			"padded tab and newline":      "\n\t Wishlist \t\n",
		}

		for name, rawName := range cases {
			t.Run(name, func(t *testing.T) {
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
						collection.PutSavedItemIntoUserCollectionResult{},
						nil,
					)

				_, err := svc.PutSavedItem(
					context.Background(),
					userID,
					savedItemID,
					rawName,
				)

				require.NoError(t, err)
			})
		}
	})

	t.Run("does not lowercase the stored display name", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		svc := collection.NewService(repo)

		userID := uuid.New()
		savedItemID := uuid.New()

		// Case-insensitive uniqueness is decided by collections_user_name_unique, so
		// the name is forwarded as submitted.
		repo.EXPECT().
			PutSavedItemIntoUserCollection(
				gomock.Any(),
				collection.PutSavedItemIntoUserCollectionParams{
					UserID:      userID,
					SavedItemID: savedItemID,
					Name:        "WiShLiSt",
				},
			).
			Return(collection.PutSavedItemIntoUserCollectionResult{}, nil)

		_, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"WiShLiSt",
		)

		require.NoError(t, err)
	})

	t.Run("does not look up the collection in Go before creating it", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		svc := collection.NewService(repo)

		userID := uuid.New()
		savedItemID := uuid.New()

		// The repository interface has no existence-check method at all, so there
		// is nothing the service could call. Existence is decided by the database
		// inside the transaction.
		repo.EXPECT().
			PutSavedItemIntoUserCollection(gomock.Any(), gomock.Any()).
			Return(collection.PutSavedItemIntoUserCollectionResult{}, nil).
			Times(1)

		_, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"wishlist",
		)

		require.NoError(t, err)
	})

	t.Run("rejects a blank name", func(t *testing.T) {
		blankNames := []string{
			"",
			" ",
			"\t",
			"\n",
			"   \t\n  ",
			" ",
			"　",
		}

		for _, rawName := range blankNames {
			t.Run(
				"blank "+strings.ReplaceAll(rawName, "\n", `\n`),
				func(t *testing.T) {
					ctrl := gomock.NewController(t)
					repo := collectionmocks.NewMockRepository(ctrl)

					svc := collection.NewService(repo)

					// Nothing is created and no collection is touched for a blank name.
					repo.EXPECT().
						PutSavedItemIntoUserCollection(
							gomock.Any(),
							gomock.Any(),
						).
						Times(0)

					_, err := svc.PutSavedItem(
						context.Background(),
						uuid.New(),
						uuid.New(),
						rawName,
					)

					require.Error(t, err)
					require.Equal(
						t,
						collection.CodeInvalidCollectionName,
						apperror.FromError(err).Code,
					)
					require.Equal(
						t,
						400,
						apperror.FromError(err).Status,
					)
				},
			)
		}
	})

	t.Run("rejects a name longer than the maximum", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		svc := collection.NewService(repo)

		repo.EXPECT().
			PutSavedItemIntoUserCollection(gomock.Any(), gomock.Any()).
			Times(0)

		_, err := svc.PutSavedItem(
			context.Background(),
			uuid.New(),
			uuid.New(),
			strings.Repeat("a", collection.CollectionNameMaxLength+1),
		)

		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeInvalidCollectionName,
			apperror.FromError(err).Code,
		)
		require.Equal(t, 400, apperror.FromError(err).Status)
	})

	t.Run("accepts a name exactly at the maximum length", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		svc := collection.NewService(repo)

		atLimit := strings.Repeat("a", collection.CollectionNameMaxLength)

		userID := uuid.New()
		savedItemID := uuid.New()

		repo.EXPECT().
			PutSavedItemIntoUserCollection(
				gomock.Any(),
				collection.PutSavedItemIntoUserCollectionParams{
					UserID:      userID,
					SavedItemID: savedItemID,
					Name:        atLimit,
				},
			).
			Return(collection.PutSavedItemIntoUserCollectionResult{}, nil)

		_, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			atLimit,
		)

		require.NoError(t, err)
	})

	t.Run("measures the length in runes, not bytes", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		svc := collection.NewService(repo)

		// Each of these runes is multiple bytes, so a byte-based limit would reject
		// a name that is well within the rune limit.
		multibyte := strings.Repeat("日", collection.CollectionNameMaxLength)

		userID := uuid.New()
		savedItemID := uuid.New()

		repo.EXPECT().
			PutSavedItemIntoUserCollection(
				gomock.Any(),
				collection.PutSavedItemIntoUserCollectionParams{
					UserID:      userID,
					SavedItemID: savedItemID,
					Name:        multibyte,
				},
			).
			Return(collection.PutSavedItemIntoUserCollectionResult{}, nil)

		_, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			multibyte,
		)

		require.NoError(t, err)
	})

	t.Run("returns repository errors unchanged", func(t *testing.T) {
		cases := map[string]error{
			"reserved name": apperror.ConflictWith(
				collection.CodeCollectionNameReserved,
				"collection name is already in use",
				nil,
			),
			"unknown or foreign saved item": apperror.NotFoundWith(
				collection.CodeSavedItemNotFound,
				"saved item not found",
				nil,
			),
			"internal": apperror.Internal(
				errors.New("connection reset"),
			),
		}

		for name, repoErr := range cases {
			t.Run(name, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				repo := collectionmocks.NewMockRepository(ctrl)

				svc := collection.NewService(repo)

				repo.EXPECT().
					PutSavedItemIntoUserCollection(gomock.Any(), gomock.Any()).
					Return(collection.PutSavedItemIntoUserCollectionResult{}, repoErr)

				_, err := svc.PutSavedItem(
					context.Background(),
					uuid.New(),
					uuid.New(),
					"Wishlist",
				)

				// The service must not wrap or reinterpret what the repository
				// decided.
				require.Equal(t, repoErr, err)
			})
		}
	})

	t.Run("passes the result flags through", func(t *testing.T) {
		cases := []struct {
			name                string
			collectionCreated   bool
			alreadyInCollection bool
		}{
			{name: "created", collectionCreated: true, alreadyInCollection: false},
			{name: "reused", collectionCreated: false, alreadyInCollection: false},
			{name: "already in collection", collectionCreated: false, alreadyInCollection: true},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				repo := collectionmocks.NewMockRepository(ctrl)

				svc := collection.NewService(repo)

				repoResult := collection.PutSavedItemIntoUserCollectionResult{
					Collection: collectiondb.Collection{
						ID: uuid.New(),
					},
					CollectionCreated:   tc.collectionCreated,
					AlreadyInCollection: tc.alreadyInCollection,
				}

				repo.EXPECT().
					PutSavedItemIntoUserCollection(gomock.Any(), gomock.Any()).
					Return(repoResult, nil)

				result, err := svc.PutSavedItem(
					context.Background(),
					uuid.New(),
					uuid.New(),
					"Wishlist",
				)

				require.NoError(t, err)
				require.Equal(t, tc.collectionCreated, result.CollectionCreated)
				require.Equal(t, tc.alreadyInCollection, result.AlreadyInCollection)
				require.Equal(t, repoResult.Collection, result.Collection)
				require.Equal(t, repoResult.SavedItem, result.SavedItem)
			})
		}
	})

	t.Run("passes the saved item and collection through", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		svc := collection.NewService(repo)

		userID := uuid.New()
		savedItemID := uuid.New()
		targetID := uuid.New()

		repoResult := collection.PutSavedItemIntoUserCollectionResult{
			Collection: collectiondb.Collection{
				ID:     targetID,
				UserID: userID,
				Name:   "Wishlist",
				Type:   "user",
			},
			CollectionCreated: true,
			SavedItem: collection.SavedItem{
				ID:           savedItemID,
				UserID:       userID,
				URL:          "https://example.com/articles/1",
				CollectionID: targetID,
			},
		}

		repo.EXPECT().
			PutSavedItemIntoUserCollection(gomock.Any(), gomock.Any()).
			Return(repoResult, nil)

		result, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"Wishlist",
		)

		require.NoError(t, err)
		require.Equal(t, repoResult.Collection, result.Collection)
		require.Equal(t, repoResult.SavedItem, result.SavedItem)
	})

	t.Run("passes the authenticated user id and the saved item id unchanged", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		svc := collection.NewService(repo)

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
			DoAndReturn(
				func(
					_ context.Context,
					params collection.PutSavedItemIntoUserCollectionParams,
				) (collection.PutSavedItemIntoUserCollectionResult, error) {
					// userID is the only identity the operation has, and it comes from
					// the access token, so it is what scopes both the saved item
					// lookup and the collection.
					require.Equal(t, userID, params.UserID)
					require.Equal(t, savedItemID, params.SavedItemID)

					return collection.PutSavedItemIntoUserCollectionResult{}, nil
				},
			)

		_, err := svc.PutSavedItem(
			context.Background(),
			userID,
			savedItemID,
			"Wishlist",
		)

		require.NoError(t, err)
	})

	t.Run("carries no enrichment state", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		svc := collection.NewService(repo)

		repo.EXPECT().
			PutSavedItemIntoUserCollection(gomock.Any(), gomock.Any()).
			Return(collection.PutSavedItemIntoUserCollectionResult{}, nil)

		_, err := svc.PutSavedItem(
			context.Background(),
			uuid.New(),
			uuid.New(),
			"Wishlist",
		)

		require.NoError(t, err)

		// The params carry a name, an owner and a saved item id, and nothing about
		// enrichment. Enrichment is a separate concern that this operation must not
		// read, validate or write, so there is nowhere for it to appear.
		paramsType := reflect.TypeOf(
			collection.PutSavedItemIntoUserCollectionParams{},
		)

		for i := range paramsType.NumField() {
			field := paramsType.Field(i).Name

			require.NotContains(t, strings.ToLower(field), "enrichment")
			require.NotContains(t, strings.ToLower(field), "last_enriched")
		}

		// Same for the returned saved item projection.
		savedItemType := reflect.TypeOf(collection.SavedItem{})

		for i := range savedItemType.NumField() {
			field := savedItemType.Field(i).Name

			require.NotContains(t, strings.ToLower(field), "enrichment")
			require.NotContains(t, strings.ToLower(field), "last_enriched")
		}
	})

	t.Run("does nothing beyond one repository call", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := collectionmocks.NewMockRepository(ctrl)

		svc := collection.NewService(repo)

		// The transaction is owned by the repository, so the service must not
		// compose several repository calls into the operation.
		repo.EXPECT().
			PutSavedItemIntoUserCollection(gomock.Any(), gomock.Any()).
			Return(collection.PutSavedItemIntoUserCollectionResult{}, nil).
			Times(1)

		_, err := svc.PutSavedItem(
			context.Background(),
			uuid.New(),
			uuid.New(),
			"Wishlist",
		)

		require.NoError(t, err)
	})
}
