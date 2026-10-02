package saved_item_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	saveditemmocks "github.com/thoriqr/stash-it-backend/internal/api/saved_item/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func TestService_Delete(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		userID := uuid.New()
		savedItemID := uuid.New()

		repo.EXPECT().
			DeleteSavedItemByIDForUser(
				gomock.Any(),
				userID,
				savedItemID,
			).
			Return(nil)

		err := svc.Delete(
			context.Background(),
			userID,
			savedItemID,
		)

		require.NoError(t, err)
	})

	t.Run("passes both user id and saved item id to the repository", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		userID := uuid.New()
		savedItemID := uuid.New()
		otherUserID := uuid.New()

		repo.EXPECT().
			DeleteSavedItemByIDForUser(
				gomock.Any(),
				userID,
				savedItemID,
			).
			DoAndReturn(
				func(
					_ context.Context,
					gotUserID uuid.UUID,
					gotSavedItemID uuid.UUID,
				) error {
					require.Equal(t, userID, gotUserID)
					require.Equal(t, savedItemID, gotSavedItemID)
					require.NotEqual(t, otherUserID, gotUserID)

					return nil
				},
			)

		err := svc.Delete(
			context.Background(),
			userID,
			savedItemID,
		)

		require.NoError(t, err)
	})

	t.Run("not found maps to resource not found", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		repo.EXPECT().
			DeleteSavedItemByIDForUser(
				gomock.Any(),
				gomock.Any(),
				gomock.Any(),
			).
			Return(apperror.NotFound(pgx.ErrNoRows))

		err := svc.Delete(
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

		svc := saved_item.NewService(repo)

		repoErr := apperror.Internal(errors.New("delete failed"))

		repo.EXPECT().
			DeleteSavedItemByIDForUser(
				gomock.Any(),
				gomock.Any(),
				gomock.Any(),
			).
			Return(repoErr)

		err := svc.Delete(
			context.Background(),
			uuid.New(),
			uuid.New(),
		)

		require.Equal(t, repoErr, err)
	})
}
