package saved_item_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	saveditemdb "github.com/thoriqr/stash-it-backend/internal/api/saved_item/generated"
	saveditemmocks "github.com/thoriqr/stash-it-backend/internal/api/saved_item/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func TestService_List(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		userID := uuid.New()

		savedItems := []saveditemdb.SavedItem{
			{ID: uuid.New(), UserID: userID},
			{ID: uuid.New(), UserID: userID},
		}

		repo.EXPECT().
			ListSavedItems(
				gomock.Any(),
				userID,
				int32(10),
				int32(10),
			).
			Return(savedItems, nil)

		repo.EXPECT().
			CountSavedItems(
				gomock.Any(),
				userID,
			).
			Return(int64(25), nil)

		result, err := svc.List(
			context.Background(),
			userID,
			2,
			10,
		)

		require.NoError(t, err)
		require.Equal(t, savedItems, result.SavedItems)
		require.Equal(t, 2, result.Page)
		require.Equal(t, 10, result.Limit)
		require.Equal(t, int64(25), result.Total)
		require.Equal(t, 3, result.TotalPages)
	})

	t.Run("page less than one uses first page", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		userID := uuid.New()

		repo.EXPECT().
			ListSavedItems(
				gomock.Any(),
				userID,
				int32(0),
				int32(10),
			).
			Return([]saveditemdb.SavedItem{}, nil)

		repo.EXPECT().
			CountSavedItems(gomock.Any(), userID).
			Return(int64(0), nil)

		result, err := svc.List(
			context.Background(),
			userID,
			0,
			10,
		)

		require.NoError(t, err)
		require.Equal(t, 1, result.Page)
		require.Equal(t, 0, result.TotalPages)
	})

	t.Run("limit above max is clamped", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		userID := uuid.New()

		repo.EXPECT().
			ListSavedItems(
				gomock.Any(),
				userID,
				int32(0),
				int32(saved_item.SavedItemListMaxLimit),
			).
			Return([]saveditemdb.SavedItem{}, nil)

		repo.EXPECT().
			CountSavedItems(gomock.Any(), userID).
			Return(int64(0), nil)

		result, err := svc.List(
			context.Background(),
			userID,
			1,
			500,
		)

		require.NoError(t, err)
		require.Equal(
			t,
			saved_item.SavedItemListMaxLimit,
			result.Limit,
		)
	})

	t.Run("limit below one uses default", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		userID := uuid.New()

		repo.EXPECT().
			ListSavedItems(
				gomock.Any(),
				userID,
				int32(0),
				int32(saved_item.SavedItemListDefaultLimit),
			).
			Return([]saveditemdb.SavedItem{}, nil)

		repo.EXPECT().
			CountSavedItems(gomock.Any(), userID).
			Return(int64(0), nil)

		result, err := svc.List(
			context.Background(),
			userID,
			1,
			0,
		)

		require.NoError(t, err)
		require.Equal(
			t,
			saved_item.SavedItemListDefaultLimit,
			result.Limit,
		)
	})

	t.Run("returns repository error unchanged", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		listErr := apperror.Internal(errors.New("list failed"))

		repo.EXPECT().
			ListSavedItems(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, listErr)

		_, err := svc.List(context.Background(), uuid.New(), 1, 10)

		require.Equal(t, listErr, err)
	})
}
