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

	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/security"
	workerenrichment "github.com/thoriqr/stash-it-backend/internal/worker/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/worker/enrichment/mocks"
)

// rawLogURL carries every component that must not reach a log: userinfo, a
// query-string token and a fragment.
const rawLogURL = "http://user:password@127.0.0.1/articles/one" +
	"?token=secret-token#section"

// newObservedLogger captures Warn and above, which is where enrichment failures
// are written.
func newObservedLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, observed := observer.New(zapcore.WarnLevel)

	return zap.New(core), observed
}

// requireSingleLog renders the one captured entry as text, message and fields
// included, so a single assertion can cover everything that was written.
func requireSingleLog(t *testing.T, observed *observer.ObservedLogs) string {
	t.Helper()

	entries := observed.All()
	require.Len(t, entries, 1)

	rendered := entries[0].Message

	for key, value := range entries[0].ContextMap() {
		rendered += " " + key + "=" + fmt.Sprint(value)
	}

	return rendered
}

// requireNoRawURLComponents fails if any part of rawLogURL reached the log.
func requireNoRawURLComponents(t *testing.T, rendered string) {
	t.Helper()

	require.NotContains(t, rendered, "token=secret-token")
	require.NotContains(t, rendered, "#section")
	require.NotContains(t, rendered, "user:")
	require.NotContains(t, rendered, "password")
}

// The real extractor over the real guarded client produces the error production
// produces. The service must log it without the request URL, even though the
// error it returns still carries one.
func TestService_EnrichSavedItem_LogsNoRawURL(t *testing.T) {
	ctrl := gomock.NewController(t)

	item := workerenrichment.SavedItem{
		ID:     uuid.New(),
		UserID: uuid.New(),
		Url:    rawLogURL,
	}

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemForBackgroundEnrichment(gomock.Any(), item.ID).
		Return(item, nil)
	repository.EXPECT().
		FailSavedItemEnrichment(gomock.Any(), item.ID).
		Return(nil)

	policy := security.DefaultOutboundFetchPolicy()

	enricher := enrichmentcore.NewEnricher(
		security.NewGuardedHTTPClient(policy),
		policy,
	)

	log, observed := newObservedLogger()

	service := workerenrichment.NewService(repository, enricher, nil, log)

	err := service.EnrichSavedItem(context.Background(), item.ID)
	require.Error(t, err)

	// The error keeps its original text, which is what the queue decision and
	// the caller see. Only the log is sanitized.
	require.Contains(t, err.Error(), "token=secret-token")

	rendered := requireSingleLog(t, observed)

	require.Contains(t, rendered, "background enrichment failed")
	require.Contains(t, rendered, "kind=content")
	require.Contains(t, rendered, "url=http://127.0.0.1/articles/one")
	require.Contains(
		t,
		rendered,
		"outbound address is not a public internet destination",
	)
	requireNoRawURLComponents(t, rendered)
}

// The handler logs the same error again for the retryable and permanent
// outcomes, so it has to apply the same sanitation.
func TestHandler_EnrichSavedItem_LogsNoRawURL(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()

	service := mocks.NewMockService(ctrl)
	service.EXPECT().
		EnrichSavedItem(gomock.Any(), savedItemID).
		Return(newTransportFailure(t, rawLogURL))

	log, observed := newObservedLogger()

	err := workerenrichment.NewHandler(service, log).EnrichSavedItem(
		context.Background(),
		newEnrichmentTask(t, savedItemID),
	)
	require.Error(t, err)

	rendered := requireSingleLog(t, observed)

	require.Contains(
		t,
		rendered,
		"background enrichment failed permanently, not retrying",
	)
	require.Contains(t, rendered, "kind=content")
	require.Contains(
		t,
		rendered,
		"outbound address is not a public internet destination",
	)
	requireNoRawURLComponents(t, rendered)
}

// newTransportFailure asks the real extractor, over the real guarded client, for
// the failure a refused destination produces.
func newTransportFailure(t *testing.T, rawURL string) error {
	t.Helper()

	policy := security.DefaultOutboundFetchPolicy()

	_, err := enrichmentcore.NewEnricher(
		security.NewGuardedHTTPClient(policy),
		policy,
	).Enrich(context.Background(), rawURL)

	require.Error(t, err)

	return err
}
