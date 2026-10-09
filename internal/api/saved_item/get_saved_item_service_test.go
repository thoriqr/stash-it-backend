package saved_item_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	saveditemmocks "github.com/thoriqr/stash-it-backend/internal/api/saved_item/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// noCollection is the zero collection every stub returns by default. Only the tests
// that care about the collection name set it.
var noCollection = saved_item.SavedItemCollection{}

func TestService_Get(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		userID := uuid.New()
		savedItemID := uuid.New()

		savedItem := saved_item.SavedItem{
			ID:       savedItemID,
			UserID:   userID,
			Url:      "https://example.com/articles/1",
			Domain:   pgtype.Text{String: "example.com", Valid: true},
			Platform: pgtype.Text{},
			Title:    pgtype.Text{},
		}

		repo.EXPECT().
			GetSavedItemByIDForUser(
				gomock.Any(),
				userID,
				savedItemID,
			).
			Return(savedItem, noCollection, nil)

		result, err := svc.Get(
			context.Background(),
			userID,
			savedItemID,
		)

		require.NoError(t, err)
		require.Equal(t, savedItem, result.SavedItem)
	})

	t.Run("passes both user id and saved item id to the repository", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		userID := uuid.New()
		savedItemID := uuid.New()
		otherUserID := uuid.New()

		repo.EXPECT().
			GetSavedItemByIDForUser(
				gomock.Any(),
				userID,
				savedItemID,
			).
			DoAndReturn(
				func(
					_ context.Context,
					gotUserID uuid.UUID,
					gotSavedItemID uuid.UUID,
				) (saved_item.SavedItem, saved_item.SavedItemCollection, error) {
					require.Equal(t, userID, gotUserID)
					require.Equal(t, savedItemID, gotSavedItemID)
					require.NotEqual(t, otherUserID, gotUserID)

					return saved_item.SavedItem{
						ID:     gotSavedItemID,
						UserID: gotUserID,
					}, noCollection, nil
				},
			)

		_, err := svc.Get(
			context.Background(),
			userID,
			savedItemID,
		)

		require.NoError(t, err)
	})

	// The collection travels with the item rather than being fetched afterwards. One
	// repository call returning both is what proves the detail response names its
	// collection without a second round trip.
	t.Run("returns the collection the repository read alongside the item", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		userID := uuid.New()
		savedItemID := uuid.New()
		collectionID := uuid.New()

		item := saved_item.SavedItem{
			ID:           savedItemID,
			UserID:       userID,
			CollectionID: collectionID,
		}

		collection := saved_item.SavedItemCollection{
			ID:   collectionID,
			Name: "YouTube",
		}

		repo.EXPECT().
			GetSavedItemByIDForUser(
				gomock.Any(),
				userID,
				savedItemID,
			).
			Return(item, collection, nil).
			Times(1)

		result, err := svc.Get(
			context.Background(),
			userID,
			savedItemID,
		)

		require.NoError(t, err)
		require.Equal(t, item, result.SavedItem)
		require.Equal(t, collectionID, result.Collection.ID)
		require.Equal(t, "YouTube", result.Collection.Name)
	})

	// Unsorted is not special-cased. The service has no branch on the collection's
	// kind, and a test that only used a user collection would not notice one
	// appearing here.
	t.Run("returns unsorted like any other collection", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		userID := uuid.New()
		savedItemID := uuid.New()
		unsortedID := uuid.New()

		repo.EXPECT().
			GetSavedItemByIDForUser(gomock.Any(), userID, savedItemID).
			Return(
				saved_item.SavedItem{ID: savedItemID, CollectionID: unsortedID},
				saved_item.SavedItemCollection{ID: unsortedID, Name: "Unsorted"},
				nil,
			)

		result, err := svc.Get(
			context.Background(),
			userID,
			savedItemID,
		)

		require.NoError(t, err)
		require.Equal(t, unsortedID, result.Collection.ID)
		require.Equal(t, "Unsorted", result.Collection.Name)
	})

	// A pending or failed enrichment is an ordinary state of a saved item. The
	// service must not branch on enrichment_status and refuse to return it.
	t.Run("returns an item whose enrichment has not run", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		userID := uuid.New()
		savedItemID := uuid.New()

		for _, status := range []string{"pending", "failed"} {
			item := saved_item.SavedItem{
				ID:               savedItemID,
				UserID:           userID,
				EnrichmentStatus: status,
				Title:            pgtype.Text{},
				Description:      pgtype.Text{},
				ImageURL:         pgtype.Text{},
				LastEnrichedAt:   pgtype.Timestamptz{},
			}

			repo.EXPECT().
				GetSavedItemByIDForUser(gomock.Any(), userID, savedItemID).
				Return(item, noCollection, nil)

			result, err := svc.Get(
				context.Background(),
				userID,
				savedItemID,
			)

			require.NoError(t, err, "status %s must still be returned", status)
			require.Equal(t, status, result.SavedItem.EnrichmentStatus)
			require.Equal(t, status, result.SavedItem.EnrichmentStatus)
		}
	})

	t.Run("not found maps to resource not found", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		repo.EXPECT().
			GetSavedItemByIDForUser(
				gomock.Any(),
				gomock.Any(),
				gomock.Any(),
			).
			Return(
				saved_item.SavedItem{},
				saved_item.SavedItemCollection{},
				apperror.NotFound(pgx.ErrNoRows),
			)

		_, err := svc.Get(
			context.Background(),
			uuid.New(),
			uuid.New(),
		)

		require.Error(t, err)
		require.Equal(
			t,
			apperror.CodeNotFound,
			apperror.FromError(err).Code,
		)
		require.Equal(t, http.StatusNotFound, apperror.FromError(err).Status)
	})

	t.Run("returns repository error unchanged", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		repoErr := apperror.Internal(errors.New("query failed"))

		repo.EXPECT().
			GetSavedItemByIDForUser(
				gomock.Any(),
				gomock.Any(),
				gomock.Any(),
			).
			Return(
				saved_item.SavedItem{},
				saved_item.SavedItemCollection{},
				repoErr,
			)

		_, err := svc.Get(
			context.Background(),
			uuid.New(),
			uuid.New(),
		)

		require.Equal(t, repoErr, err)
	})
}
