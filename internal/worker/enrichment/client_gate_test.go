package enrichment_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/hostset"
	workerenrichment "github.com/thoriqr/stash-it-backend/internal/worker/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/worker/enrichment/mocks"
)

// A stale task for an item the client already completed must not overwrite it
// with a failure.
func TestService_EnrichSavedItem_LeavesAClientCompletedItemAlone(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()
	userID := uuid.New()

	repository := mocks.NewMockRepository(ctrl)
	enricher := mocks.NewMockMetadataEnricher(ctrl)

	repository.EXPECT().
		GetSavedItemForBackgroundEnrichment(gomock.Any(), savedItemID).
		Return(workerenrichment.SavedItem{
			ID:               savedItemID,
			UserID:           userID,
			Url:              "https://www.instagram.com/reel/abc/",
			EnrichmentStatus: "completed",
		}, nil)

	enricher.EXPECT().Enrich(gomock.Any(), gomock.Any()).Times(0)

	// No FailSavedItemEnrichment call is expected: gomock fails the test if the
	// worker tries to mark a completed item failed.

	service := workerenrichment.NewService(
		repository,
		enricher,
		nil,
		hostset.New([]string{"www.instagram.com"}),
		zap.NewNop(),
	)

	require.NoError(t, service.EnrichSavedItem(context.Background(), savedItemID))
}

// A task for a host the client owns must not fetch. It records the failure and
// returns nil so the queue does not retry.
func TestService_EnrichSavedItem_SkipsClientEnrichmentHost(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()
	userID := uuid.New()

	repository := mocks.NewMockRepository(ctrl)
	enricher := mocks.NewMockMetadataEnricher(ctrl)

	repository.EXPECT().
		GetSavedItemForBackgroundEnrichment(gomock.Any(), savedItemID).
		Return(workerenrichment.SavedItem{
			ID:     savedItemID,
			UserID: userID,
			Url:    "https://www.instagram.com/reel/abc/",
		}, nil)

	// The whole point: the worker never fetches this host.
	enricher.EXPECT().Enrich(gomock.Any(), gomock.Any()).Times(0)

	repository.EXPECT().
		FailSavedItemEnrichment(gomock.Any(), savedItemID).
		Return(nil)

	service := workerenrichment.NewService(
		repository,
		enricher,
		nil,
		hostset.New([]string{"www.instagram.com"}),
		zap.NewNop(),
	)

	// nil, not an error: a retry would hit the same host again.
	require.NoError(t, service.EnrichSavedItem(context.Background(), savedItemID))
}

// A host that is not allowlisted is enriched exactly as before.
func TestService_EnrichSavedItem_NonClientHostStillFetches(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()
	userID := uuid.New()

	repository := mocks.NewMockRepository(ctrl)
	enricher := mocks.NewMockMetadataEnricher(ctrl)

	repository.EXPECT().
		GetSavedItemForBackgroundEnrichment(gomock.Any(), savedItemID).
		Return(workerenrichment.SavedItem{
			ID:     savedItemID,
			UserID: userID,
			Url:    "https://example.com/article",
		}, nil)

	title := "From the page"
	enricher.EXPECT().
		Enrich(gomock.Any(), "https://example.com/article").
		Return(enrichmentcore.Metadata{Title: &title}, nil)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			params workerenrichment.CompleteSavedItemEnrichmentParams,
		) (workerenrichment.CompletedSavedItem, error) {
			require.Equal(t, title, *params.Title)

			return workerenrichment.CompletedSavedItem{
				SavedItemID: savedItemID,
				UserID:      userID,
			}, nil
		})

	service := workerenrichment.NewService(
		repository,
		enricher,
		nil,
		hostset.New([]string{"www.instagram.com"}),
		zap.NewNop(),
	)

	require.NoError(t, service.EnrichSavedItem(context.Background(), savedItemID))
}
