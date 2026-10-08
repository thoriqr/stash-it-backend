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

func TestService_Delete(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		userID := uuid.New()
		savedItemID := uuid.New()
		collectionID := uuid.New()

		repo.EXPECT().
			DeleteSavedItemByIDForUser(
				gomock.Any(),
				userID,
				savedItemID,
			).
			Return(saved_item.DeletedSavedItem{
				ID:           savedItemID,
				CollectionID: collectionID,
			}, nil)

		repo.EXPECT().
			CountSavedItemsInCollection(
				gomock.Any(),
				userID,
				collectionID,
			).
			Return(int64(0), nil)

		repo.EXPECT().
			GetCollectionSystemKeyForUser(
				gomock.Any(),
				userID,
				collectionID,
			).
			Return(pgtype.Text{String: "youtube", Valid: true}, nil)

		result, err := svc.Delete(
			context.Background(),
			userID,
			savedItemID,
		)

		require.NoError(t, err)
		require.Equal(t, collectionID, result.CollectionID)
		require.True(t, result.CollectionEmpty)
		require.True(t, result.CollectionDeletable)
	})

	t.Run("passes both user id and saved item id to the repository", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

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
				) (saved_item.DeletedSavedItem, error) {
					require.Equal(t, userID, gotUserID)
					require.Equal(t, savedItemID, gotSavedItemID)
					require.NotEqual(t, otherUserID, gotUserID)

					return saved_item.DeletedSavedItem{
						ID:           gotSavedItemID,
						CollectionID: uuid.New(),
					}, nil
				},
			)

		repo.EXPECT().
			CountSavedItemsInCollection(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(int64(1), nil)

		repo.EXPECT().
			GetCollectionSystemKeyForUser(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(pgtype.Text{}, nil)

		_, err := svc.Delete(
			context.Background(),
			userID,
			savedItemID,
		)

		require.NoError(t, err)
	})

	// The collection that gets counted and identified has to be the one the
	// deleted row was actually in, and it has to be scoped by the authenticated
	// user. Reading any other collection, or reading one by id alone, would report
	// on a collection this delete has nothing to do with.
	t.Run("reads the collection the deleted item was in, scoped by user", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		userID := uuid.New()
		savedItemID := uuid.New()
		collectionID := uuid.New()
		otherCollectionID := uuid.New()
		otherUserID := uuid.New()

		gomock.InOrder(
			repo.EXPECT().
				DeleteSavedItemByIDForUser(gomock.Any(), userID, savedItemID).
				Return(saved_item.DeletedSavedItem{
					ID:           savedItemID,
					CollectionID: collectionID,
				}, nil),

			repo.EXPECT().
				CountSavedItemsInCollection(
					gomock.Any(),
					userID,
					collectionID,
				).
				DoAndReturn(
					func(
						_ context.Context,
						gotUserID uuid.UUID,
						gotCollectionID uuid.UUID,
					) (int64, error) {
						require.Equal(t, userID, gotUserID)
						require.Equal(t, collectionID, gotCollectionID)
						require.NotEqual(t, otherUserID, gotUserID)
						require.NotEqual(t, otherCollectionID, gotCollectionID)

						return int64(0), nil
					},
				),

			repo.EXPECT().
				GetCollectionSystemKeyForUser(
					gomock.Any(),
					userID,
					collectionID,
				).
				DoAndReturn(
					func(
						_ context.Context,
						gotUserID uuid.UUID,
						gotCollectionID uuid.UUID,
					) (pgtype.Text, error) {
						require.Equal(t, userID, gotUserID)
						require.Equal(t, collectionID, gotCollectionID)
						require.NotEqual(t, otherUserID, gotUserID)

						return pgtype.Text{}, nil
					},
				),
		)

		result, err := svc.Delete(
			context.Background(),
			userID,
			savedItemID,
		)

		require.NoError(t, err)
		require.Equal(t, collectionID, result.CollectionID)
	})

	// The count is read after the delete, so a collection that still holds other
	// items is reported as not empty and the caller is not told it may delete it.
	t.Run("reports the collection as not empty while it still holds items", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		userID := uuid.New()
		savedItemID := uuid.New()
		collectionID := uuid.New()

		repo.EXPECT().
			DeleteSavedItemByIDForUser(gomock.Any(), userID, savedItemID).
			Return(saved_item.DeletedSavedItem{
				ID:           savedItemID,
				CollectionID: collectionID,
			}, nil)

		repo.EXPECT().
			CountSavedItemsInCollection(gomock.Any(), userID, collectionID).
			Return(int64(1), nil)

		repo.EXPECT().
			GetCollectionSystemKeyForUser(gomock.Any(), userID, collectionID).
			Return(pgtype.Text{}, nil)

		result, err := svc.Delete(
			context.Background(),
			userID,
			savedItemID,
		)

		require.NoError(t, err)
		require.False(t, result.CollectionEmpty)
		require.False(t, result.CollectionDeletable)
	})

	// Type is not the criterion. A system collection emptied by deleting its last
	// item is one the user may delete, exactly like one of their own.
	t.Run("reports a system collection other than Unsorted as deletable", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		userID := uuid.New()
		savedItemID := uuid.New()
		collectionID := uuid.New()

		repo.EXPECT().
			DeleteSavedItemByIDForUser(gomock.Any(), userID, savedItemID).
			Return(saved_item.DeletedSavedItem{
				ID:           savedItemID,
				CollectionID: collectionID,
			}, nil)

		repo.EXPECT().
			CountSavedItemsInCollection(gomock.Any(), userID, collectionID).
			Return(int64(0), nil)

		repo.EXPECT().
			GetCollectionSystemKeyForUser(gomock.Any(), userID, collectionID).
			Return(pgtype.Text{
				String: "youtube",
				Valid:  true,
			}, nil)

		result, err := svc.Delete(
			context.Background(),
			userID,
			savedItemID,
		)

		require.NoError(t, err)
		require.True(t, result.CollectionEmpty)
		require.True(t, result.CollectionDeletable)
	})

	// Unsorted is the one collection that is never deletable. It is recognised by
	// its stable key and by nothing else: its type is 'system' like any other
	// system collection, and its display name is not read at all.
	t.Run("reports an emptied Unsorted collection as empty but not deletable", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		userID := uuid.New()
		savedItemID := uuid.New()
		collectionID := uuid.New()

		repo.EXPECT().
			DeleteSavedItemByIDForUser(gomock.Any(), userID, savedItemID).
			Return(saved_item.DeletedSavedItem{
				ID:           savedItemID,
				CollectionID: collectionID,
			}, nil)

		repo.EXPECT().
			CountSavedItemsInCollection(gomock.Any(), userID, collectionID).
			Return(int64(0), nil)

		repo.EXPECT().
			GetCollectionSystemKeyForUser(gomock.Any(), userID, collectionID).
			Return(pgtype.Text{
				String: saved_item.CollectionSystemKeyUnsorted,
				Valid:  true,
			}, nil)

		result, err := svc.Delete(
			context.Background(),
			userID,
			savedItemID,
		)

		require.NoError(t, err)
		require.True(t, result.CollectionEmpty)
		require.False(t, result.CollectionDeletable)
	})

	// A collection with no key at all is one the user created. Its unset key must
	// not be compared, so it is reported as deletable like any other emptied
	// collection.
	t.Run("reports an emptied user collection with no key as deletable", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		userID := uuid.New()
		savedItemID := uuid.New()
		collectionID := uuid.New()

		repo.EXPECT().
			DeleteSavedItemByIDForUser(gomock.Any(), userID, savedItemID).
			Return(saved_item.DeletedSavedItem{
				ID:           savedItemID,
				CollectionID: collectionID,
			}, nil)

		repo.EXPECT().
			CountSavedItemsInCollection(gomock.Any(), userID, collectionID).
			Return(int64(0), nil)

		repo.EXPECT().
			GetCollectionSystemKeyForUser(gomock.Any(), userID, collectionID).
			Return(pgtype.Text{}, nil)

		result, err := svc.Delete(
			context.Background(),
			userID,
			savedItemID,
		)

		require.NoError(t, err)
		require.True(t, result.CollectionEmpty)
		require.True(t, result.CollectionDeletable)
	})

	t.Run("not found maps to resource not found", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		repo.EXPECT().
			DeleteSavedItemByIDForUser(
				gomock.Any(),
				gomock.Any(),
				gomock.Any(),
			).
			Return(
				saved_item.DeletedSavedItem{},
				apperror.NotFound(pgx.ErrNoRows),
			)

		_, err := svc.Delete(
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

	// A saved item that is missing and one owned by another user are the same
	// error, and neither may reach the collection: reading it would be a second
	// thing that differs between the two cases.
	t.Run("not found leaves the collection unread", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		repo.EXPECT().
			DeleteSavedItemByIDForUser(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(
				saved_item.DeletedSavedItem{},
				apperror.NotFound(pgx.ErrNoRows),
			)

		result, err := svc.Delete(
			context.Background(),
			uuid.New(),
			uuid.New(),
		)

		require.Error(t, err)
		require.Equal(t, saved_item.DeleteResult{}, result)
	})

	t.Run("returns the delete error unchanged", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		repoErr := apperror.Internal(errors.New("delete failed"))

		repo.EXPECT().
			DeleteSavedItemByIDForUser(
				gomock.Any(),
				gomock.Any(),
				gomock.Any(),
			).
			Return(saved_item.DeletedSavedItem{}, repoErr)

		_, err := svc.Delete(
			context.Background(),
			uuid.New(),
			uuid.New(),
		)

		require.Equal(t, repoErr, err)
	})

	t.Run("returns the count error unchanged", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		collectionID := uuid.New()
		repoErr := apperror.Internal(errors.New("count failed"))

		repo.EXPECT().
			DeleteSavedItemByIDForUser(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(saved_item.DeletedSavedItem{
				ID:           uuid.New(),
				CollectionID: collectionID,
			}, nil)

		repo.EXPECT().
			CountSavedItemsInCollection(
				gomock.Any(),
				gomock.Any(),
				collectionID,
			).
			Return(int64(0), repoErr)

		_, err := svc.Delete(
			context.Background(),
			uuid.New(),
			uuid.New(),
		)

		require.Equal(t, repoErr, err)
	})

	t.Run("returns the system key error unchanged", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		collectionID := uuid.New()
		repoErr := apperror.Internal(errors.New("system key failed"))

		repo.EXPECT().
			DeleteSavedItemByIDForUser(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(saved_item.DeletedSavedItem{
				ID:           uuid.New(),
				CollectionID: collectionID,
			}, nil)

		repo.EXPECT().
			CountSavedItemsInCollection(gomock.Any(), gomock.Any(), collectionID).
			Return(int64(0), nil)

		repo.EXPECT().
			GetCollectionSystemKeyForUser(
				gomock.Any(),
				gomock.Any(),
				collectionID,
			).
			Return(pgtype.Text{}, repoErr)

		_, err := svc.Delete(
			context.Background(),
			uuid.New(),
			uuid.New(),
		)

		require.Equal(t, repoErr, err)
	})

	// The delete is committed by the time the collection is read, and nothing
	// else happens: no collection is written and the result is reported, not acted
	// on. Exactly three repository calls, each once.
	t.Run("does nothing beyond one delete and two reads", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		collectionID := uuid.New()

		repo.EXPECT().
			DeleteSavedItemByIDForUser(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(saved_item.DeletedSavedItem{
				ID:           uuid.New(),
				CollectionID: collectionID,
			}, nil).
			Times(1)

		repo.EXPECT().
			CountSavedItemsInCollection(
				gomock.Any(),
				gomock.Any(),
				collectionID,
			).
			Return(int64(0), nil).
			Times(1)

		repo.EXPECT().
			GetCollectionSystemKeyForUser(
				gomock.Any(),
				gomock.Any(),
				collectionID,
			).
			Return(pgtype.Text{}, nil).
			Times(1)

		_, err := svc.Delete(
			context.Background(),
			uuid.New(),
			uuid.New(),
		)

		require.NoError(t, err)
	})
}
