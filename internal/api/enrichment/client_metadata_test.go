package enrichment_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/api/enrichment/mocks"
	"github.com/thoriqr/stash-it-backend/internal/clientmetadata"
	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/hostset"
)

// newClientService builds the service with a host allowlist, which is what makes
// the enrich endpoint treat a host as client-owned.
func newClientService(
	t *testing.T,
	repository enrichment.Repository,
	enricher enrichment.MetadataEnricher,
) enrichment.Service {
	t.Helper()

	return enrichment.NewService(
		repository,
		enricher,
		hostset.NewPlatformMap(map[string]string{"www.instagram.com": "Instagram", "instagram.com": "Instagram"}),
		zap.NewNop(),
	)
}

func clientText(value string) *string {
	return &value
}

// A client-extracted result is stored and the server never touches the page.
func TestService_Enrich_ClientHostStoresMetadataWithoutFetching(t *testing.T) {
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()

	repository := mocks.NewMockRepository(ctrl)
	enricher := mocks.NewMockMetadataEnricher(ctrl)

	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return("https://www.instagram.com/reel/abc/", nil)

	// The whole point: no outbound fetch for this host.
	enricher.EXPECT().Enrich(gomock.Any(), gomock.Any()).Times(0)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			params enrichment.CompleteSavedItemEnrichmentParams,
		) (enrichment.SavedItem, error) {
			require.Equal(t, userID, params.UserID)
			require.Equal(t, savedItemID, params.SavedItemID)
			require.Equal(t, "A reel", *params.Title)
			require.Equal(t, "A description", *params.Description)
			require.Equal(t, "https://cdn.example/preview.jpg", *params.ImageURL)
			require.Equal(t, "Instagram", *params.Platform)

			// Price is never accepted from a client.
			require.Nil(t, params.PriceAmount)
			require.Nil(t, params.PriceCurrency)

			return enrichment.SavedItem{
				ID:               savedItemID,
				EnrichmentStatus: "completed",
			}, nil
		})

	result, err := newClientService(t, repository, enricher).Enrich(
		context.Background(),
		userID,
		savedItemID,
		&clientmetadata.Metadata{
			Title:       clientText("A reel"),
			Description: clientText("A description"),
			ImageURL:    clientText("https://cdn.example/preview.jpg"),
		},
	)

	require.NoError(t, err)
	require.Equal(t, "completed", result.SavedItem.EnrichmentStatus)
}

// One usable field is enough to complete the item; the rest are stored NULL.
func TestService_Enrich_ClientHostCompletesOnOneField(t *testing.T) {
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()

	repository := mocks.NewMockRepository(ctrl)
	enricher := mocks.NewMockMetadataEnricher(ctrl)

	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return("https://instagram.com/p/abc/", nil)

	enricher.EXPECT().Enrich(gomock.Any(), gomock.Any()).Times(0)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			params enrichment.CompleteSavedItemEnrichmentParams,
		) (enrichment.SavedItem, error) {
			require.Equal(t, "Only a title", *params.Title)
			require.Nil(t, params.Description)
			require.Nil(t, params.ImageURL)
			require.Equal(t, "Instagram", *params.Platform)

			return enrichment.SavedItem{
				ID:               savedItemID,
				EnrichmentStatus: "completed",
			}, nil
		})

	_, err := newClientService(t, repository, enricher).Enrich(
		context.Background(),
		userID,
		savedItemID,
		&clientmetadata.Metadata{Title: clientText("Only a title")},
	)

	require.NoError(t, err)
}

// Nothing extracted is a failed enrichment, and still no fetch.
func TestService_Enrich_ClientHostWithoutMetadataFails(t *testing.T) {
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()

	repository := mocks.NewMockRepository(ctrl)
	enricher := mocks.NewMockMetadataEnricher(ctrl)

	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return("https://www.instagram.com/reel/abc/", nil)

	enricher.EXPECT().Enrich(gomock.Any(), gomock.Any()).Times(0)

	repository.EXPECT().
		FailSavedItemEnrichment(gomock.Any(), userID, savedItemID).
		Return(enrichment.SavedItem{
			ID:               savedItemID,
			EnrichmentStatus: "failed",
		}, nil)

	result, err := newClientService(t, repository, enricher).Enrich(
		context.Background(),
		userID,
		savedItemID,
		nil,
	)

	require.NoError(t, err)
	require.Equal(t, "failed", result.SavedItem.EnrichmentStatus)
}

// Blank values count as absent, so whitespace is not a stored value.
func TestService_Enrich_ClientHostBlankValuesFail(t *testing.T) {
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()

	repository := mocks.NewMockRepository(ctrl)
	enricher := mocks.NewMockMetadataEnricher(ctrl)

	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return("https://www.instagram.com/reel/abc/", nil)

	enricher.EXPECT().Enrich(gomock.Any(), gomock.Any()).Times(0)

	repository.EXPECT().
		FailSavedItemEnrichment(gomock.Any(), userID, savedItemID).
		Return(enrichment.SavedItem{
			ID:               savedItemID,
			EnrichmentStatus: "failed",
		}, nil)

	_, err := newClientService(t, repository, enricher).Enrich(
		context.Background(),
		userID,
		savedItemID,
		&clientmetadata.Metadata{
			Title:       clientText("   "),
			Description: clientText("\n\t"),
		},
	)

	require.NoError(t, err)
}

// A non-http image is rejected before anything is written.
func TestService_Enrich_ClientHostRejectsNonHTTPImage(t *testing.T) {
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()

	repository := mocks.NewMockRepository(ctrl)
	enricher := mocks.NewMockMetadataEnricher(ctrl)

	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return("https://www.instagram.com/reel/abc/", nil)

	enricher.EXPECT().Enrich(gomock.Any(), gomock.Any()).Times(0)

	// No write is expected.
	_, err := newClientService(t, repository, enricher).Enrich(
		context.Background(),
		userID,
		savedItemID,
		&clientmetadata.Metadata{
			ImageURL: clientText("javascript:alert(1)"),
		},
	)

	requireAppError(t, err, 400, "VALIDATION_ERROR")
}

// An over-long value is rejected rather than truncated.
func TestService_Enrich_ClientHostRejectsOversizedTitle(t *testing.T) {
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()

	repository := mocks.NewMockRepository(ctrl)
	enricher := mocks.NewMockMetadataEnricher(ctrl)

	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return("https://www.instagram.com/reel/abc/", nil)

	enricher.EXPECT().Enrich(gomock.Any(), gomock.Any()).Times(0)

	oversized := make([]rune, 1001)
	for index := range oversized {
		oversized[index] = 'a'
	}

	_, err := newClientService(t, repository, enricher).Enrich(
		context.Background(),
		userID,
		savedItemID,
		&clientmetadata.Metadata{Title: clientText(string(oversized))},
	)

	requireAppError(t, err, 400, "VALIDATION_ERROR")
}

// Instagram's Open Graph title is a caption with an author prefix. The client path
// applies the same shortening the server extractor does.
func TestService_Enrich_ClientHostShortensInstagramTitle(t *testing.T) {
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()

	repository := mocks.NewMockRepository(ctrl)
	enricher := mocks.NewMockMetadataEnricher(ctrl)

	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return("https://www.instagram.com/reel/abc/", nil)

	enricher.EXPECT().Enrich(gomock.Any(), gomock.Any()).Times(0)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			params enrichment.CompleteSavedItemEnrichmentParams,
		) (enrichment.SavedItem, error) {
			require.Equal(t, "Shark Detail on Instagram", *params.Title)
			require.Equal(t, "a caption", *params.Description)

			return enrichment.SavedItem{
				ID:               savedItemID,
				EnrichmentStatus: "completed",
			}, nil
		})

	_, err := newClientService(t, repository, enricher).Enrich(
		context.Background(),
		userID,
		savedItemID,
		&clientmetadata.Metadata{
			Title:       clientText("Shark Detail on Instagram: \"a caption\""),
			Description: clientText("a caption"),
		},
	)

	require.NoError(t, err)
}

// The body is ignored for a host the server fetches itself, which is what keeps
// an older or newer client from changing server-side enrichment by accident.
func TestService_Enrich_NonClientHostIgnoresMetadata(t *testing.T) {
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()

	repository := mocks.NewMockRepository(ctrl)
	enricher := mocks.NewMockMetadataEnricher(ctrl)

	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return("https://example.com/article", nil)

	enricher.EXPECT().
		Enrich(gomock.Any(), "https://example.com/article").
		Return(enrichmentcore.Metadata{Title: clientText("From the server")}, nil)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			params enrichment.CompleteSavedItemEnrichmentParams,
		) (enrichment.SavedItem, error) {
			require.Equal(t, "From the server", *params.Title)

			return enrichment.SavedItem{
				ID:               savedItemID,
				EnrichmentStatus: "completed",
			}, nil
		})

	result, err := newClientService(t, repository, enricher).Enrich(
		context.Background(),
		userID,
		savedItemID,
		&clientmetadata.Metadata{Title: clientText("From the client")},
	)

	require.NoError(t, err)
	require.Equal(t, "completed", result.SavedItem.EnrichmentStatus)
}
