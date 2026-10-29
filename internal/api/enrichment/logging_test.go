package enrichment_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/thoriqr/stash-it-backend/internal/api/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/api/enrichment/mocks"
	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// The synchronous path uses the same extractor and the same failure, so it has
// the same obligation: the log may name the page's kind and cause, but not the
// request URL with its credentials and parameters.
func TestService_Enrich_LogsNoRawURL(t *testing.T) {
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()

	rawURL := "http://user:password@127.0.0.1/articles/one" +
		"?token=secret-token#section"

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return(rawURL, nil)
	repository.EXPECT().
		FailSavedItemEnrichment(gomock.Any(), userID, savedItemID).
		Return(enrichment.SavedItem{
			ID:               savedItemID,
			UserID:           userID,
			Url:              rawURL,
			EnrichmentStatus: "failed",
		}, nil)

	policy := security.DefaultOutboundFetchPolicy()

	enricher := enrichmentcore.NewEnricher(
		security.NewGuardedHTTPClient(policy),
		policy,
	)

	core, observed := observer.New(zapcore.WarnLevel)

	result, err := enrichment.NewService(
		repository,
		enricher,
		zap.New(core),
	).Enrich(context.Background(), userID, savedItemID)

	require.NoError(t, err)
	require.Equal(t, "failed", result.SavedItem.EnrichmentStatus)

	entries := observed.All()
	require.Len(t, entries, 1)

	rendered := entries[0].Message

	for key, value := range entries[0].ContextMap() {
		rendered += " " + key + "=" + fmt.Sprint(value)
	}

	require.Contains(t, rendered, "enrichment failed")
	require.Contains(t, rendered, "kind=content")
	require.Contains(t, rendered, "url=http://127.0.0.1/articles/one")
	require.Contains(
		t,
		rendered,
		"outbound address is not a public internet destination",
	)

	require.NotContains(t, rendered, "token=secret-token")
	require.NotContains(t, rendered, "#section")
	require.NotContains(t, rendered, "user:")
	require.NotContains(t, rendered, "password")
}
