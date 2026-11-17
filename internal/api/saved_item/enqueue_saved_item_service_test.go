package saved_item_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	saveditemdb "github.com/thoriqr/stash-it-backend/internal/api/saved_item/generated"
	saveditemmocks "github.com/thoriqr/stash-it-backend/internal/api/saved_item/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/clientmetadata"
	"github.com/thoriqr/stash-it-backend/internal/hostset"
)

// A host the client fetches itself is stored but never queued: the worker must
// not fetch it, and the metadata arrives through the enrich endpoint instead.
func TestService_Create_DoesNotEnqueueAClientEnrichmentHost(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)
	enqueuer := saveditemmocks.NewMockSavedItemEnqueuer(ctrl)

	svc := saved_item.NewService(
		repo,
		enqueuer,
		hostset.New([]string{"www.instagram.com"}),
		zap.NewNop(),
	)

	userID := uuid.New()
	savedItemID := uuid.New()

	repo.EXPECT().
		GetUnsortedCollectionByUser(gomock.Any(), userID).
		Return(uuid.New(), nil)

	repo.EXPECT().
		CreateClientEnrichedSavedItem(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			params saveditemdb.CreateClientEnrichedSavedItemParams,
		) (saved_item.SavedItem, error) {
			require.Equal(t, "failed", params.EnrichmentStatus)
			require.False(t, params.Title.Valid)
			require.False(t, params.Description.Valid)
			require.False(t, params.ImageUrl.Valid)
			require.False(t, params.Platform.Valid)

			return saved_item.SavedItem{ID: savedItemID, UserID: userID}, nil
		})

	// No EnqueueSavedItemEnrichment call is expected: gomock fails the test if one
	// happens, which is exactly the assertion this test needs.

	result, err := svc.Create(
		context.Background(),
		userID,
		"https://www.instagram.com/reel/abc/",
		nil,
	)

	require.NoError(t, err)
	require.Equal(t, savedItemID, result.SavedItem.ID)
}

// Client metadata on an allowlisted host is stored with the row, completed, and
// still not queued.
func TestService_Create_StoresClientMetadataWithoutEnqueue(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)
	enqueuer := saveditemmocks.NewMockSavedItemEnqueuer(ctrl)

	svc := saved_item.NewService(
		repo,
		enqueuer,
		hostset.New([]string{"www.instagram.com"}),
		zap.NewNop(),
	)

	userID := uuid.New()
	savedItemID := uuid.New()

	repo.EXPECT().
		GetUnsortedCollectionByUser(gomock.Any(), userID).
		Return(uuid.New(), nil)

	repo.EXPECT().
		CreateClientEnrichedSavedItem(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			params saveditemdb.CreateClientEnrichedSavedItemParams,
		) (saved_item.SavedItem, error) {
			require.Equal(t, "completed", params.EnrichmentStatus)
			require.Equal(t, "A reel", params.Title.String)
			require.Equal(t, "A description", params.Description.String)
			require.Equal(t, "https://cdn.example/preview.jpg", params.ImageUrl.String)
			require.Equal(t, "Instagram", params.Platform.String)

			return saved_item.SavedItem{
				ID:               savedItemID,
				UserID:           userID,
				EnrichmentStatus: "completed",
			}, nil
		})

	// No EnqueueSavedItemEnrichment call is expected.

	result, err := svc.CreateWithMetadata(
		context.Background(),
		userID,
		"https://www.instagram.com/reel/abc/",
		nil,
		&clientmetadata.Metadata{
			Title:       textPtr("A reel"),
			Description: textPtr("A description"),
			ImageURL:    textPtr("https://cdn.example/preview.jpg"),
			Platform:    textPtr("Instagram"),
		},
	)

	require.NoError(t, err)
	require.Equal(t, "completed", result.SavedItem.EnrichmentStatus)
}

// A non-allowlisted host ignores the body entirely and keeps the server path.
func TestService_Create_IgnoresMetadataForNonClientHost(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)
	enqueuer := saveditemmocks.NewMockSavedItemEnqueuer(ctrl)

	svc := saved_item.NewService(
		repo,
		enqueuer,
		hostset.New([]string{"instagram.com"}),
		zap.NewNop(),
	)

	userID := uuid.New()
	savedItemID := uuid.New()

	repo.EXPECT().
		GetUnsortedCollectionByUser(gomock.Any(), userID).
		Return(uuid.New(), nil)

	repo.EXPECT().
		CreateSavedItem(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			params saveditemdb.CreateSavedItemParams,
		) (saved_item.SavedItem, error) {
			// The normal path still leaves these to enrichment.
			require.False(t, params.Title.Valid)
			require.False(t, params.Platform.Valid)

			return saved_item.SavedItem{ID: savedItemID, UserID: userID}, nil
		})

	enqueuer.EXPECT().
		EnqueueSavedItemEnrichment(gomock.Any(), savedItemID).
		Return(nil)

	_, err := svc.CreateWithMetadata(
		context.Background(),
		userID,
		"https://example.com/article",
		nil,
		&clientmetadata.Metadata{Title: textPtr("From the client")},
	)

	require.NoError(t, err)
}

func textPtr(value string) *string {
	return &value
}

// A lookalike host is not the allowlisted host, so it is still queued.
func TestService_Create_EnqueuesALookalikeHost(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)
	enqueuer := saveditemmocks.NewMockSavedItemEnqueuer(ctrl)

	svc := saved_item.NewService(
		repo,
		enqueuer,
		hostset.New([]string{"instagram.com"}),
		zap.NewNop(),
	)

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
		Return(nil)

	_, err := svc.Create(
		context.Background(),
		userID,
		"https://notinstagram.com/reel/abc/",
		nil,
	)

	require.NoError(t, err)
}

func TestService_Create_EnqueuesAfterCommit(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)
	enqueuer := saveditemmocks.NewMockSavedItemEnqueuer(ctrl)

	svc := saved_item.NewService(repo, enqueuer, nil, zap.NewNop())

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
		nil,
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

	svc := saved_item.NewService(repo, enqueuer, nil, zap.NewNop())

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
		nil,
	)

	require.Equal(t, writeErr, err)
}

// The same holds for a failure earlier in the flow: a request rejected before
// the insert must not leave a task behind.
func TestService_Create_DoesNotEnqueueWhenTheURLIsRejected(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)
	enqueuer := saveditemmocks.NewMockSavedItemEnqueuer(ctrl)

	svc := saved_item.NewService(repo, enqueuer, nil, zap.NewNop())

	repo.EXPECT().
		CreateSavedItem(gomock.Any(), gomock.Any()).
		Times(0)

	enqueuer.EXPECT().
		EnqueueSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Times(0)

	_, err := svc.Create(context.Background(), uuid.New(), "not-a-url", nil)

	require.Error(t, err)
}

// A queue that cannot be reached must not turn a committed save into a failed
// request. The saved item is valid product data whether or not its metadata was
// ever fetched, so the caller is told the save worked.
func TestService_Create_SurvivesAnUnreachableQueue(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)
	enqueuer := saveditemmocks.NewMockSavedItemEnqueuer(ctrl)

	svc := saved_item.NewService(repo, enqueuer, nil, zap.NewNop())

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
		nil,
	)

	require.NoError(t, err)
	require.Equal(t, savedItemID, result.SavedItem.ID)
}

// A process with no queue configured still saves. This is what makes the enqueue
// an enhancement rather than a dependency of the core loop.
func TestService_Create_WorksWithoutAnEnqueuer(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)

	svc := saved_item.NewService(repo, nil, nil, zap.NewNop())

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
		nil,
	)

	require.NoError(t, err)
	require.Equal(t, savedItemID, result.SavedItem.ID)
}

// Reading and deleting an item must never enqueue anything. Only saving starts
// enrichment, and a task per delete would enrich items on their way out.
//
// Listing an item's collection is the same case in a different slice: it is a read,
// so it schedules nothing either. That is covered by the collection package's own
// tests, since listing moved there.
func TestService_DoesNotEnqueueOnReadsOrDeletes(t *testing.T) {
	ctrl := gomock.NewController(t)

	repo := saveditemmocks.NewMockRepository(ctrl)
	enqueuer := saveditemmocks.NewMockSavedItemEnqueuer(ctrl)

	svc := saved_item.NewService(repo, enqueuer, nil, zap.NewNop())

	userID := uuid.New()
	savedItemID := uuid.New()

	enqueuer.EXPECT().
		EnqueueSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Times(0)

	repo.EXPECT().
		GetSavedItemByIDForUser(gomock.Any(), userID, savedItemID).
		Return(
			saved_item.SavedItem{ID: savedItemID},
			saved_item.SavedItemCollection{ID: uuid.New(), Name: "Unsorted"},
			nil,
		)

	_, err := svc.Get(context.Background(), userID, savedItemID)
	require.NoError(t, err)

	// The delete now also reads the collection the item was in, to report whether
	// it became empty. Those are reads: a delete still schedules no work at all.
	repo.EXPECT().
		DeleteSavedItemByIDForUser(gomock.Any(), userID, savedItemID).
		Return(saved_item.DeletedSavedItem{
			ID:           savedItemID,
			CollectionID: uuid.New(),
		}, nil)

	repo.EXPECT().
		CountSavedItemsInCollection(gomock.Any(), userID, gomock.Any()).
		Return(int64(0), nil)

	repo.EXPECT().
		GetCollectionSystemKeyForUser(gomock.Any(), userID, gomock.Any()).
		Return(pgtype.Text{}, nil)

	_, err = svc.Delete(context.Background(), userID, savedItemID)
	require.NoError(t, err)
}
