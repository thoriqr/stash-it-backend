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

	"github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	saveditemdb "github.com/thoriqr/stash-it-backend/internal/api/saved_item/generated"
	saveditemmocks "github.com/thoriqr/stash-it-backend/internal/api/saved_item/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func TestService_Get(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		userID := uuid.New()
		savedItemID := uuid.New()

		savedItem := saveditemdb.SavedItem{
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
			Return(savedItem, nil)

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

		svc := saved_item.NewService(repo)

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
				) (saveditemdb.SavedItem, error) {
					require.Equal(t, userID, gotUserID)
					require.Equal(t, savedItemID, gotSavedItemID)
					require.NotEqual(t, otherUserID, gotUserID)

					return saveditemdb.SavedItem{
						ID:     gotSavedItemID,
						UserID: gotUserID,
					}, nil
				},
			)

		_, err := svc.Get(
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
			GetSavedItemByIDForUser(
				gomock.Any(),
				gomock.Any(),
				gomock.Any(),
			).
			Return(
				saveditemdb.SavedItem{},
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

		svc := saved_item.NewService(repo)

		repoErr := apperror.Internal(errors.New("query failed"))

		repo.EXPECT().
			GetSavedItemByIDForUser(
				gomock.Any(),
				gomock.Any(),
				gomock.Any(),
			).
			Return(saveditemdb.SavedItem{}, repoErr)

		_, err := svc.Get(
			context.Background(),
			uuid.New(),
			uuid.New(),
		)

		require.Equal(t, repoErr, err)
	})
}
