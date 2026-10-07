package saved_item_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	saveditemmocks "github.com/thoriqr/stash-it-backend/internal/api/saved_item/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func TestService_Create_EnqueuesAfterCommit(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)
	enqueuer := saveditemmocks.NewMockSavedItemEnqueuer(ctrl)

	svc := saved_item.NewService(repo, enqueuer, zap.NewNop())

	userID := uuid.New()
	savedItemID := uuid.New()

	repo.EXPECT().
		GetUnsortedCollectionByUser(gomock.Any(), userID).
		Return(uuid.New(), nil)

	repo.EXPECT().
		CreateSavedItem(gomock.Any(), gomock.Any()).
		Return(saved_item.SavedItem{ID: savedItemID, UserID: userID}, nil)

	// Exactly one task, naming the committed row and nothing else.
	enqueuer.EXPECT().
		EnqueueSavedItemEnrichment(gomock.Any(), savedItemID).
		Return(nil)

	result, err := svc.Create(
		context.Background(),
		userID,
		"https://example.com/articles/1",
	)

	require.NoError(t, err)
	require.Equal(t, savedItemID, result.SavedItem.ID)
}

// No task is queued for a write that failed, because the id the payload would
// carry was never committed. This is the whole reason the enqueue is not done
// before the insert.
func TestService_Create_DoesNotEnqueueWhenTheWriteFails(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)
	enqueuer := saveditemmocks.NewMockSavedItemEnqueuer(ctrl)

	svc := saved_item.NewService(repo, enqueuer, zap.NewNop())

	userID := uuid.New()

	writeErr := apperror.Internal(errors.New("insert failed"))

	repo.EXPECT().
		GetUnsortedCollectionByUser(gomock.Any(), userID).
		Return(uuid.New(), nil)

	repo.EXPECT().
		CreateSavedItem(gomock.Any(), gomock.Any()).
		Return(saved_item.SavedItem{}, writeErr)

	enqueuer.EXPECT().
		EnqueueSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Times(0)

	_, err := svc.Create(
		context.Background(),
		userID,
		"https://example.com/articles/1",
	)

	require.Equal(t, writeErr, err)
}

// The same holds for a failure earlier in the flow: a request rejected before
// the insert must not leave a task behind.
func TestService_Create_DoesNotEnqueueWhenTheURLIsRejected(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)
	enqueuer := saveditemmocks.NewMockSavedItemEnqueuer(ctrl)

	svc := saved_item.NewService(repo, enqueuer, zap.NewNop())

	repo.EXPECT().
		CreateSavedItem(gomock.Any(), gomock.Any()).
		Times(0)

	enqueuer.EXPECT().
		EnqueueSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Times(0)

	_, err := svc.Create(context.Background(), uuid.New(), "not-a-url")

	require.Error(t, err)
}

// A queue that cannot be reached must not turn a committed save into a failed
// request. The saved item is valid product data whether or not its metadata was
// ever fetched, so the caller is told the save worked.
func TestService_Create_SurvivesAnUnreachableQueue(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)
	enqueuer := saveditemmocks.NewMockSavedItemEnqueuer(ctrl)

	svc := saved_item.NewService(repo, enqueuer, zap.NewNop())

	userID := uuid.New()
	savedItemID := uuid.New()

	repo.EXPECT().
		GetUnsortedCollectionByUser(gomock.Any(), userID).
		Return(uuid.New(), nil)

	repo.EXPECT().
		CreateSavedItem(gomock.Any(), gomock.Any()).
		Return(saved_item.SavedItem{ID: savedItemID, UserID: userID}, nil)

	enqueuer.EXPECT().
		EnqueueSavedItemEnrichment(gomock.Any(), savedItemID).
		Return(errors.New("dial tcp 127.0.0.1:6379: connect: connection refused"))

	result, err := svc.Create(
		context.Background(),
		userID,
		"https://example.com/articles/1",
	)

	require.NoError(t, err)
	require.Equal(t, savedItemID, result.SavedItem.ID)
}

// A process with no queue configured still saves. This is what makes the enqueue
// an enhancement rather than a dependency of the core loop.
func TestService_Create_WorksWithoutAnEnqueuer(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)

	svc := saved_item.NewService(repo, nil, zap.NewNop())

	userID := uuid.New()
	savedItemID := uuid.New()

	repo.EXPECT().
		GetUnsortedCollectionByUser(gomock.Any(), userID).
		Return(uuid.New(), nil)

	repo.EXPECT().
		CreateSavedItem(gomock.Any(), gomock.Any()).
		Return(saved_item.SavedItem{ID: savedItemID, UserID: userID}, nil)

	result, err := svc.Create(
		context.Background(),
		userID,
		"https://example.com/articles/1",
	)

	require.NoError(t, err)
	require.Equal(t, savedItemID, result.SavedItem.ID)
}

// Reading and deleting an item must never enqueue anything. Only saving starts
// enrichment, and a task per delete would enrich items on their way out.
func TestService_DoesNotEnqueueOnReadsOrDeletes(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)
	enqueuer := saveditemmocks.NewMockSavedItemEnqueuer(ctrl)

	svc := saved_item.NewService(repo, enqueuer, zap.NewNop())

	userID := uuid.New()
	savedItemID := uuid.New()

	enqueuer.EXPECT().
		EnqueueSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Times(0)

	repo.EXPECT().
		GetSavedItemByIDForUser(gomock.Any(), userID, savedItemID).
		Return(saved_item.SavedItem{ID: savedItemID}, nil)

	_, err := svc.Get(context.Background(), userID, savedItemID)
	require.NoError(t, err)

	repo.EXPECT().
		ListSavedItems(gomock.Any(), userID, gomock.Any(), gomock.Any()).
		Return([]saved_item.SavedItem{}, nil)

	repo.EXPECT().
		CountSavedItems(gomock.Any(), userID).
		Return(int64(0), nil)

	_, err = svc.List(context.Background(), userID, 1, 20)
	require.NoError(t, err)

	repo.EXPECT().
		DeleteSavedItemByIDForUser(gomock.Any(), userID, savedItemID).
		Return(nil)

	require.NoError(
		t,
		svc.Delete(context.Background(), userID, savedItemID),
	)
}
