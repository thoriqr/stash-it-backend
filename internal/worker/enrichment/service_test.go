package enrichment_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	workerenrichment "github.com/thoriqr/stash-it-backend/internal/worker/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/worker/enrichment/mocks"
)

func validText(value string) *string {
	return &value
}

func newTestService(
	repository workerenrichment.Repository,
	enricher workerenrichment.MetadataEnricher,
) workerenrichment.Service {
	// zap.NewNop keeps the failure log out of test output. The service logs the
	// enrichment error precisely because it is not swallowed, so it has to be given
	// a usable logger.
	//
	// The organizer is nil in most cases so that a scheduling failure cannot
	// change the outcome these tests are about. The handoff has its own tests.
	return workerenrichment.NewService(repository, enricher, nil, zap.NewNop())
}

// newTestServiceWithOrganizer builds the service with a real organizer, for the
// tests that assert what a successful enrichment schedules.
func newTestServiceWithOrganizer(
	repository workerenrichment.Repository,
	enricher workerenrichment.MetadataEnricher,
	organizer workerenrichment.SavedItemOrganizer,
) workerenrichment.Service {
	return workerenrichment.NewService(
		repository,
		enricher,
		organizer,
		zap.NewNop(),
	)
}

// fetchFailure and contentFailure build the two classifications that decide retry
// behaviour, using the real error type from the extraction package rather than a
// local stand-in. A test that invented its own failure type would pass whether or
// not the worker reads the classification the extractor actually attaches.
func fetchFailure(cause error) error {
	return &enrichmentcore.Failure{
		Kind: enrichmentcore.FailureFetch,
		Err:  cause,
	}
}

func contentFailure(cause error) error {
	return &enrichmentcore.Failure{
		Kind: enrichmentcore.FailureContent,
		Err:  cause,
	}
}

func parseFailure(cause error) error {
	return &enrichmentcore.Failure{
		Kind: enrichmentcore.FailureParse,
		Err:  cause,
	}
}

func savedItem(rawURL string) workerenrichment.SavedItem {
	return workerenrichment.SavedItem{
		ID:     uuid.New(),
		UserID: uuid.New(),
		Url:    rawURL,
	}
}

func TestService_EnrichSavedItem_Completes(t *testing.T) {
	ctrl := gomock.NewController(t)

	item := savedItem("https://example.com/articles/1")
	savedItemID := item.ID

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemForBackgroundEnrichment(gomock.Any(), savedItemID).
		Return(item, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), "https://example.com/articles/1").
		Return(enrichmentcore.Metadata{
			Title:       validText("An interesting article"),
			Platform:    validText("example"),
			Description: validText("A short summary"),
			ImageURL:    validText("https://example.com/og.png"),
		}, nil)

	// Only the metadata columns and the status are written. There is no
	// collection_id, url or domain in the parameters, so the statement cannot
	// touch them.
	repository.EXPECT().
		CompleteSavedItemEnrichment(
			gomock.Any(),
			workerenrichment.CompleteSavedItemEnrichmentParams{
				SavedItemID: savedItemID,
				Title:       validText("An interesting article"),
				Platform:    validText("example"),
				Description: validText("A short summary"),
				ImageURL:    validText("https://example.com/og.png"),
			},
		).
		Return(completed(savedItemID, validText("example")), nil)

	repository.EXPECT().
		FailSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Times(0)

	err := newTestService(repository, enricher).
		EnrichSavedItem(context.Background(), savedItemID)

	require.NoError(t, err)
}

// completed builds the repository's result for a successful enrichment write,
// using the platform the extractor reported.
func completed(
	savedItemID uuid.UUID,
	platform *string,
) workerenrichment.CompletedSavedItem {
	return workerenrichment.CompletedSavedItem{
		SavedItemID: savedItemID,
		UserID:      uuid.New(),
		Platform:    platform,
	}
}

// A page that exposes nothing is a successful enrichment with an empty result,
// so it is recorded as completed rather than failed.
func TestService_EnrichSavedItem_EmptyMetadataIsCompleted(t *testing.T) {
	ctrl := gomock.NewController(t)

	item := savedItem("https://example.com/silent")
	savedItemID := item.ID

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemForBackgroundEnrichment(gomock.Any(), savedItemID).
		Return(item, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), gomock.Any()).
		Return(enrichmentcore.Metadata{}, nil)

	// Every metadata value is a nil pointer, which is how the repository writes
	// NULL. That is the whole point: a page that said nothing must clear stale
	// values rather than leave the previous ones standing.
	repository.EXPECT().
		CompleteSavedItemEnrichment(
			gomock.Any(),
			workerenrichment.CompleteSavedItemEnrichmentParams{
				SavedItemID: savedItemID,
				Title:       nil,
				Platform:    nil,
				Description: nil,
				ImageURL:    nil,
			},
		).
		Return(completed(savedItemID, nil), nil)

	err := newTestService(repository, enricher).
		EnrichSavedItem(context.Background(), savedItemID)

	require.NoError(t, err)
}

// A missing item is reported with the sentinel the handler discards on, so the
// enricher is never reached for a row that is gone.
func TestService_EnrichSavedItem_MissingItem(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()

	notFound := apperror.NotFoundWith(
		workerenrichment.CodeSavedItemNotFound,
		"saved item not found",
		workerenrichment.ErrSavedItemNotFound,
	)

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemForBackgroundEnrichment(gomock.Any(), savedItemID).
		Return(workerenrichment.SavedItem{}, notFound)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), gomock.Any()).
		Times(0)

	repository.EXPECT().
		FailSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Times(0)

	err := newTestService(repository, enricher).
		EnrichSavedItem(context.Background(), savedItemID)

	require.ErrorIs(t, err, workerenrichment.ErrSavedItemNotFound)
}

// A failed enrichment is recorded as failed and its classified error is returned
// unchanged, so the handler reads the classification the extractor attached.
func TestService_EnrichSavedItem_Failure(t *testing.T) {
	cause := errors.New("connection reset by peer")
	enrichErr := fetchFailure(cause)

	cases := []struct {
		name string
		err  error
	}{
		{
			name: "fetch failure",
			err:  enrichErr,
		},
		{
			name: "content failure",
			err:  contentFailure(errors.New("status 404")),
		},
		{
			name: "parse failure",
			err:  parseFailure(errors.New("unreadable document")),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)

			item := savedItem("https://example.com/gone")
			savedItemID := item.ID

			repository := mocks.NewMockRepository(ctrl)
			repository.EXPECT().
				GetSavedItemForBackgroundEnrichment(gomock.Any(), savedItemID).
				Return(item, nil)

			enricher := mocks.NewMockMetadataEnricher(ctrl)
			enricher.EXPECT().
				Enrich(gomock.Any(), item.Url).
				Return(enrichmentcore.Metadata{}, tc.err)

			// The failure is recorded on the item. Nothing writes metadata, which
			// is what preserves whatever the item already carried, and
			// last_enriched_at is untouched because that column is only written by
			// the completion path.
			repository.EXPECT().
				FailSavedItemEnrichment(gomock.Any(), savedItemID).
				Return(nil)

			repository.EXPECT().
				CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
				Times(0)

			err := newTestService(repository, enricher).
				EnrichSavedItem(context.Background(), savedItemID)

			require.ErrorIs(t, err, tc.err)
		})
	}
}

// The status write is the response to a failed enrichment, so if it fails the
// service reports that instead. Returning the page error would leave the item
// silently claiming to be pending while the queue moved on.
func TestService_EnrichSavedItem_FailStatusWriteErrorWins(t *testing.T) {
	ctrl := gomock.NewController(t)

	item := savedItem("https://example.com/gone")
	savedItemID := item.ID

	writeErr := apperror.Internal(errors.New("database unavailable"))

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemForBackgroundEnrichment(gomock.Any(), savedItemID).
		Return(item, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), item.Url).
		Return(enrichmentcore.Metadata{}, contentFailure(errors.New("status 500")))

	repository.EXPECT().
		FailSavedItemEnrichment(gomock.Any(), savedItemID).
		Return(writeErr)

	err := newTestService(repository, enricher).
		EnrichSavedItem(context.Background(), savedItemID)

	require.Equal(t, writeErr, err)
}

func TestService_EnrichSavedItem_CompleteWriteError(t *testing.T) {
	ctrl := gomock.NewController(t)

	item := savedItem("https://example.com/articles/1")
	savedItemID := item.ID

	writeErr := apperror.Internal(errors.New("deadlock detected"))

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemForBackgroundEnrichment(gomock.Any(), savedItemID).
		Return(item, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), item.Url).
		Return(enrichmentcore.Metadata{Title: validText("A title")}, nil)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Return(workerenrichment.CompletedSavedItem{}, writeErr)

	err := newTestService(repository, enricher).
		EnrichSavedItem(context.Background(), savedItemID)

	require.Equal(t, writeErr, err)
}

// An item deleted between the load and the write is reported with the same
// sentinel as one that was already gone, because it is the same situation.
func TestService_EnrichSavedItem_DeletedDuringWrite(t *testing.T) {
	ctrl := gomock.NewController(t)

	item := savedItem("https://example.com/articles/1")
	savedItemID := item.ID

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemForBackgroundEnrichment(gomock.Any(), savedItemID).
		Return(item, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), item.Url).
		Return(enrichmentcore.Metadata{Title: validText("A title")}, nil)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Return(
			workerenrichment.CompletedSavedItem{},
			apperror.NotFoundWith(
				workerenrichment.CodeSavedItemNotFound,
				"saved item not found",
				workerenrichment.ErrSavedItemNotFound,
			),
		)

	err := newTestService(repository, enricher).
		EnrichSavedItem(context.Background(), savedItemID)

	require.ErrorIs(t, err, workerenrichment.ErrSavedItemNotFound)
}

// The enricher must never see a context or a URL that the repository did not
// supply: the worker reads the URL from the row rather than from the payload.
func TestService_EnrichSavedItem_EnrichesTheStoredURL(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()

	item := workerenrichment.SavedItem{
		ID:     savedItemID,
		UserID: uuid.New(),
		Url:    "https://stored.example.com/canonical",
	}

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemForBackgroundEnrichment(gomock.Any(), savedItemID).
		Return(item, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), "https://stored.example.com/canonical").
		Return(enrichmentcore.Metadata{}, nil)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Return(
			workerenrichment.CompletedSavedItem{
				SavedItemID: savedItemID,
				UserID:      item.UserID,
			},
			nil,
		)

	err := newTestService(repository, enricher).
		EnrichSavedItem(context.Background(), savedItemID)

	require.NoError(t, err)
}
