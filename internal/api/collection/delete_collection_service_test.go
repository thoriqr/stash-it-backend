package collection_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/collection"
	collectionmocks "github.com/thoriqr/stash-it-backend/internal/api/collection/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func newDeleteCollectionService(
	t *testing.T,
) (collection.Service, *collectionmocks.MockRepository) {
	t.Helper()

	repo := collectionmocks.NewMockRepository(gomock.NewController(t))

	return collection.NewService(repo), repo
}

func TestService_DeleteCollection(t *testing.T) {
	t.Run("deletes an empty user collection", func(t *testing.T) {
		svc, repo := newDeleteCollectionService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		repo.EXPECT().
			DeleteCollection(gomock.Any(), collection.DeleteCollectionParams{
				UserID:       userID,
				CollectionID: collectionID,
				Action:       collection.SavedItemsActionDelete,
			}).
			Return(nil)

		require.NoError(t, svc.DeleteCollection(context.Background(),
			collection.DeleteCollectionParams{
				UserID:       userID,
				CollectionID: collectionID,
				Action:       collection.SavedItemsActionDelete,
			},
		))
	})

	// Type is not the criterion, so the action carries no type information at all
	// and the service cannot be filtering on it.
	t.Run("deletes an empty system collection the same way", func(t *testing.T) {
		svc, repo := newDeleteCollectionService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		repo.EXPECT().
			DeleteCollection(gomock.Any(), collection.DeleteCollectionParams{
				UserID:       userID,
				CollectionID: collectionID,
				Action:       collection.SavedItemsActionDelete,
			}).
			Return(nil)

		require.NoError(t, svc.DeleteCollection(context.Background(),
			collection.DeleteCollectionParams{
				UserID:       userID,
				CollectionID: collectionID,
				Action:       collection.SavedItemsActionDelete,
			},
		))
	})

	t.Run("passes the target through for a move", func(t *testing.T) {
		svc, repo := newDeleteCollectionService(t)

		userID := uuid.New()
		collectionID := uuid.New()
		targetCollectionID := uuid.New()

		repo.EXPECT().
			DeleteCollection(gomock.Any(), collection.DeleteCollectionParams{
				UserID:             userID,
				CollectionID:       collectionID,
				Action:             collection.SavedItemsActionMove,
				TargetCollectionID: targetCollectionID,
			}).
			Return(nil)

		require.NoError(t, svc.DeleteCollection(context.Background(),
			collection.DeleteCollectionParams{
				UserID:             userID,
				CollectionID:       collectionID,
				Action:             collection.SavedItemsActionMove,
				TargetCollectionID: targetCollectionID,
			},
		))
	})

	// Unsorted is an ordinary target here. The one thing that is special about it
	// is that it cannot itself be the collection being deleted, and the repository
	// decides that by its system_key with no help from the request.
	t.Run("accepts the unsorted collection as a move target by id", func(t *testing.T) {
		svc, repo := newDeleteCollectionService(t)

		userID := uuid.New()
		collectionID := uuid.New()

		repo.EXPECT().
			DeleteCollection(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params collection.DeleteCollectionParams,
				) error {
					require.Equal(
						t,
						collection.SavedItemsActionMove,
						params.Action,
					)

					// The target is just an id. Nothing in the request says this is
					// Unsorted, and nothing special is done to it.
					require.NotEqual(t, uuid.Nil, params.TargetCollectionID)
					require.NotEqual(
						t,
						params.CollectionID,
						params.TargetCollectionID,
					)

					return nil
				},
			)

		require.NoError(t, svc.DeleteCollection(context.Background(),
			collection.DeleteCollectionParams{
				UserID:             userID,
				CollectionID:       collectionID,
				Action:             collection.SavedItemsActionMove,
				TargetCollectionID: uuid.New(),
			},
		))
	})

	// Every rejection below happens before the repository is called. That is the
	// point of them: a request that says nothing coherent about the user's saved
	// items must not reach a statement that could act on it.
	t.Run("rejects a target given with the delete action", func(t *testing.T) {
		svc, repo := newDeleteCollectionService(t)

		repo.EXPECT().
			DeleteCollection(gomock.Any(), gomock.Any()).
			Times(0)

		err := svc.DeleteCollection(context.Background(),
			collection.DeleteCollectionParams{
				UserID:             uuid.New(),
				CollectionID:       uuid.New(),
				Action:             collection.SavedItemsActionDelete,
				TargetCollectionID: uuid.New(),
			},
		)

		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeInvalidCollectionDeleteTarget,
			apperror.FromError(err).Code,
		)
		require.Equal(t, http.StatusBadRequest, apperror.FromError(err).Status)
	})

	t.Run("rejects a move with no target", func(t *testing.T) {
		svc, repo := newDeleteCollectionService(t)

		repo.EXPECT().
			DeleteCollection(gomock.Any(), gomock.Any()).
			Times(0)

		err := svc.DeleteCollection(context.Background(),
			collection.DeleteCollectionParams{
				UserID:       uuid.New(),
				CollectionID: uuid.New(),
				Action:       collection.SavedItemsActionMove,
			},
		)

		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeInvalidCollectionDeleteTarget,
			apperror.FromError(err).Code,
		)
		require.Equal(t, http.StatusBadRequest, apperror.FromError(err).Status)
	})

	// Moving items into the collection being deleted is not a move: the items would
	// be reassigned to a row that is about to be removed.
	t.Run("rejects a target equal to the collection being deleted", func(t *testing.T) {
		svc, repo := newDeleteCollectionService(t)

		repo.EXPECT().
			DeleteCollection(gomock.Any(), gomock.Any()).
			Times(0)

		collectionID := uuid.New()

		err := svc.DeleteCollection(context.Background(),
			collection.DeleteCollectionParams{
				UserID:             uuid.New(),
				CollectionID:       collectionID,
				Action:             collection.SavedItemsActionMove,
				TargetCollectionID: collectionID,
			},
		)

		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeInvalidCollectionDeleteTarget,
			apperror.FromError(err).Code,
		)
		require.Equal(t, http.StatusBadRequest, apperror.FromError(err).Status)
	})

	// The two actions are opposites in their consequences and one of them destroys
	// content, so nothing is defaulted on the caller's behalf.
	t.Run("rejects a missing action", func(t *testing.T) {
		svc, repo := newDeleteCollectionService(t)

		repo.EXPECT().
			DeleteCollection(gomock.Any(), gomock.Any()).
			Times(0)

		err := svc.DeleteCollection(context.Background(),
			collection.DeleteCollectionParams{
				UserID:       uuid.New(),
				CollectionID: uuid.New(),
			},
		)

		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeInvalidCollectionDeleteAction,
			apperror.FromError(err).Code,
		)
		require.Equal(t, http.StatusBadRequest, apperror.FromError(err).Status)
	})

	t.Run("rejects an unrecognised action", func(t *testing.T) {
		svc, repo := newDeleteCollectionService(t)

		repo.EXPECT().
			DeleteCollection(gomock.Any(), gomock.Any()).
			Times(0)

		err := svc.DeleteCollection(context.Background(),
			collection.DeleteCollectionParams{
				UserID:       uuid.New(),
				CollectionID: uuid.New(),
				Action:       collection.SavedItemsAction("archive"),
			},
		)

		require.Error(t, err)
		require.Equal(
			t,
			collection.CodeInvalidCollectionDeleteAction,
			apperror.FromError(err).Code,
		)
		require.Equal(t, http.StatusBadRequest, apperror.FromError(err).Status)
	})

	// These are decided by the repository against the database, and the service
	// passes them through untouched. Asserting them here proves the service adds no
	// opinion of its own about which collections may be deleted.
	t.Run("passes a not found error through", func(t *testing.T) {
		svc, repo := newDeleteCollectionService(t)

		repoErr := apperror.NotFoundWith(
			collection.CodeCollectionNotFound,
			"collection not found",
			errors.New("no rows"),
		)

		repo.EXPECT().
			DeleteCollection(gomock.Any(), gomock.Any()).
			Return(repoErr)

		err := svc.DeleteCollection(context.Background(),
			collection.DeleteCollectionParams{
				UserID:       uuid.New(),
				CollectionID: uuid.New(),
				Action:       collection.SavedItemsActionDelete,
			},
		)

		require.Equal(t, repoErr, err)
	})

	t.Run("passes the unsorted collection error through", func(t *testing.T) {
		svc, repo := newDeleteCollectionService(t)

		repoErr := apperror.ConflictWith(
			collection.CodeUnsortedCollectionProtected,
			"the unsorted collection cannot be deleted",
			nil,
		)

		repo.EXPECT().
			DeleteCollection(gomock.Any(), gomock.Any()).
			Return(repoErr)

		err := svc.DeleteCollection(context.Background(),
			collection.DeleteCollectionParams{
				UserID:       uuid.New(),
				CollectionID: uuid.New(),
				Action:       collection.SavedItemsActionDelete,
			},
		)

		require.Equal(t, repoErr, err)
	})

	t.Run("passes a target not found error through", func(t *testing.T) {
		svc, repo := newDeleteCollectionService(t)

		repoErr := apperror.NotFoundWith(
			collection.CodeCollectionDeleteTargetNotFound,
			"target collection not found",
			errors.New("no rows"),
		)

		repo.EXPECT().
			DeleteCollection(gomock.Any(), gomock.Any()).
			Return(repoErr)

		err := svc.DeleteCollection(context.Background(),
			collection.DeleteCollectionParams{
				UserID:             uuid.New(),
				CollectionID:       uuid.New(),
				Action:             collection.SavedItemsActionMove,
				TargetCollectionID: uuid.New(),
			},
		)

		require.Equal(t, repoErr, err)
	})

	t.Run("passes a not empty error through", func(t *testing.T) {
		svc, repo := newDeleteCollectionService(t)

		repoErr := apperror.ConflictWith(
			collection.CodeCollectionNotEmpty,
			"collection is not empty",
			errors.New("violates foreign key constraint"),
		)

		repo.EXPECT().
			DeleteCollection(gomock.Any(), gomock.Any()).
			Return(repoErr)

		err := svc.DeleteCollection(context.Background(),
			collection.DeleteCollectionParams{
				UserID:       uuid.New(),
				CollectionID: uuid.New(),
				Action:       collection.SavedItemsActionDelete,
			},
		)

		require.Equal(t, repoErr, err)
	})

	// The service owns the request rules and nothing else. The transaction, the lock
	// ordering and the foreign key handling all belong to the repository, so a
	// validated request is passed straight through with no further work.
	t.Run("delegates to the repository and does nothing else", func(t *testing.T) {
		svc, repo := newDeleteCollectionService(t)

		calls := 0

		repo.EXPECT().
			DeleteCollection(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(context.Context, collection.DeleteCollectionParams) error {
					calls++

					return nil
				},
			).
			Times(1)

		require.NoError(t, svc.DeleteCollection(context.Background(),
			collection.DeleteCollectionParams{
				UserID:       uuid.New(),
				CollectionID: uuid.New(),
				Action:       collection.SavedItemsActionDelete,
			},
		))

		require.Equal(t, 1, calls)
	})
}
