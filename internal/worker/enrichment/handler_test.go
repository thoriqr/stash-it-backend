package enrichment_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	workerenrichment "github.com/thoriqr/stash-it-backend/internal/worker/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/worker/enrichment/mocks"
	"github.com/thoriqr/stash-it-backend/internal/worker/queue"
)

func newEnrichmentTask(
	t *testing.T,
	savedItemID uuid.UUID,
) *asynq.Task {
	t.Helper()

	payload, err := json.Marshal(queue.NewEnrichSavedItemPayload(savedItemID))
	require.NoError(t, err)

	return asynq.NewTask(queue.TaskTypeEnrichSavedItem, payload)
}

func newTestHandler(
	service workerenrichment.Service,
) *workerenrichment.Handler {
	// The handler logs every outcome, including the ones that are reported as
	// success, so it is given a logger rather than left nil.
	return workerenrichment.NewHandler(service, zap.NewNop())
}

func TestHandler_EnrichSavedItem_Success(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()

	service := mocks.NewMockService(ctrl)
	service.EXPECT().
		EnrichSavedItem(gomock.Any(), savedItemID).
		Return(nil)

	err := newTestHandler(service).EnrichSavedItem(
		context.Background(),
		newEnrichmentTask(t, savedItemID),
	)

	require.NoError(t, err)
}

// A deleted item is a finished task. The handler returns nil so Asynq discards
// it, and the test asserts that by also requiring the error not to carry
// SkipRetry: Asynq treats a task that returns nil as done, and one that returns
// SkipRetry as archived. Neither is a retry, but only nil means there is nothing
// left to look at.
func TestHandler_EnrichSavedItem_DeletedItemIsDiscarded(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()

	service := mocks.NewMockService(ctrl)
	service.EXPECT().
		EnrichSavedItem(gomock.Any(), savedItemID).
		Return(apperror.NotFoundWith(
			workerenrichment.CodeSavedItemNotFound,
			"saved item not found",
			workerenrichment.ErrSavedItemNotFound,
		))

	err := newTestHandler(service).EnrichSavedItem(
		context.Background(),
		newEnrichmentTask(t, savedItemID),
	)

	require.NoError(t, err)
	require.NotErrorIs(t, err, asynq.SkipRetry)
}

// The classification decides the retry, and it is read from the extraction
// package rather than re-derived here.
func TestHandler_EnrichSavedItem_RetryClassification(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		retryable bool
	}{
		{
			name: "fetch failure is retried",
			err: fetchFailure(
				errors.New("dial tcp: lookup timed out"),
			),
			retryable: true,
		},
		{
			// A rate-limited origin is not available now. This arrives from the
			// extraction package as a fetch failure precisely so it stays
			// retryable, so the worker must not reclassify it.
			name: "rate limited origin is retried",
			err: fetchFailure(
				errors.New("status 429"),
			),
			retryable: true,
		},
		{
			name: "unavailable origin is retried",
			err: fetchFailure(
				errors.New("status 503"),
			),
			retryable: true,
		},
		{
			// A body that dropped mid-read is a network failure, not a statement
			// about the page.
			name: "unreadable body is retried",
			err: fetchFailure(
				errors.New("connection reset by peer"),
			),
			retryable: true,
		},
		{
			name: "unsupported content is not retried",
			err: contentFailure(
				errors.New("response is not an html document"),
			),
			retryable: false,
		},
		{
			name: "unexpected status is not retried",
			err: contentFailure(
				errors.New("status 404"),
			),
			retryable: false,
		},
		{
			name: "gone status is not retried",
			err: contentFailure(
				errors.New("status 410"),
			),
			retryable: false,
		},
		{
			name: "unreadable document is not retried",
			err: parseFailure(
				errors.New("reading the page failed"),
			),
			retryable: false,
		},
		{
			// An error carrying no classification is treated as retryable, which
			// is what the extraction package documents for an unclassified failure
			// and what a database error looks like from here.
			name: "unclassified failure is retried",
			err: apperror.Internal(
				errors.New("connection reset"),
			),
			retryable: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)

			savedItemID := uuid.New()

			service := mocks.NewMockService(ctrl)
			service.EXPECT().
				EnrichSavedItem(gomock.Any(), savedItemID).
				Return(tc.err)

			err := newTestHandler(service).EnrichSavedItem(
				context.Background(),
				newEnrichmentTask(t, savedItemID),
			)

			require.Error(t, err)

			if tc.retryable {
				require.NotErrorIs(
					t,
					err,
					asynq.SkipRetry,
					"a retryable failure must be left to Asynq's retry mechanism",
				)

				// The original error stays reachable, so the reason is not lost by
				// the classification decision.
				require.ErrorIs(t, err, tc.err)

				return
			}

			require.ErrorIs(
				t,
				err,
				asynq.SkipRetry,
				"a permanent failure must stop the retries",
			)
			require.ErrorIs(
				t,
				err,
				tc.err,
				"the recorded failure must still be reportable",
			)
		})
	}
}

// A payload that cannot be read is discarded rather than retried, because no
// amount of waiting makes unreadable JSON readable and there is no id in it to
// work from.
func TestHandler_EnrichSavedItem_UnreadablePayload(t *testing.T) {
	ctrl := gomock.NewController(t)

	service := mocks.NewMockService(ctrl)
	service.EXPECT().
		EnrichSavedItem(gomock.Any(), gomock.Any()).
		Times(0)

	err := newTestHandler(service).EnrichSavedItem(
		context.Background(),
		asynq.NewTask(
			queue.TaskTypeEnrichSavedItem,
			[]byte("not json at all"),
		),
	)

	require.Error(t, err)
	require.ErrorIs(t, err, asynq.SkipRetry)
}

// A payload with no id is the same situation: there is nothing to enrich, so the
// task is finished.
func TestHandler_EnrichSavedItem_PayloadWithoutID(t *testing.T) {
	ctrl := gomock.NewController(t)

	payload, marshalErr := json.Marshal(queue.EnrichSavedItemPayload{})
	require.NoError(t, marshalErr)

	service := mocks.NewMockService(ctrl)
	service.EXPECT().
		EnrichSavedItem(gomock.Any(), gomock.Any()).
		Times(0)

	err := newTestHandler(service).EnrichSavedItem(
		context.Background(),
		asynq.NewTask(queue.TaskTypeEnrichSavedItem, payload),
	)

	require.Error(t, err)
	require.ErrorIs(t, err, asynq.SkipRetry)
}

// ProcessTask is the method Asynq calls, and it must behave exactly as the named
// method does. Asserting both keeps the adapter from becoming a second behaviour.
func TestHandler_ProcessTask(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()

	service := mocks.NewMockService(ctrl)
	service.EXPECT().
		EnrichSavedItem(gomock.Any(), savedItemID).
		Return(nil)

	err := newTestHandler(service).ProcessTask(
		context.Background(),
		newEnrichmentTask(t, savedItemID),
	)

	require.NoError(t, err)
}

// The handler must satisfy Asynq's interface, which is the only way the queue can
// call it at all.
func TestHandler_ImplementsAsynqHandler(t *testing.T) {
	var handler any = workerenrichment.NewHandler(nil, zap.NewNop())

	_, ok := handler.(asynq.Handler)

	require.True(t, ok, "handler must satisfy asynq.Handler")
}

// The payload carries the id and nothing else, which is what makes it impossible
// for a queued URL to disagree with the stored one.
func TestHandler_PayloadCarriesOnlyTheID(t *testing.T) {
	payload := queue.NewEnrichSavedItemPayload(uuid.New())

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded map[string]any

	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Len(t, decoded, 1)
	require.Contains(t, decoded, "saved_item_id")
}

// The classification the handler acts on is the one the extraction package
// produces, so the two paths cannot disagree about what a page error means.
func TestHandler_UsesTheExtractionPackagesClassification(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()

	service := mocks.NewMockService(ctrl)
	service.EXPECT().
		EnrichSavedItem(gomock.Any(), savedItemID).
		Return(fetchFailure(enrichmentcore.ErrFetchFailed))

	err := newTestHandler(service).EnrichSavedItem(
		context.Background(),
		newEnrichmentTask(t, savedItemID),
	)

	require.Error(t, err)
	require.ErrorIs(t, err, enrichmentcore.ErrFetchFailed)
	require.NotErrorIs(t, err, asynq.SkipRetry)
}
