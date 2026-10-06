package enrichment_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/api/enrichment/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
)

func newTestService(
	t *testing.T,
	repository enrichment.Repository,
	enricher enrichment.MetadataEnricher,
) enrichment.Service {
	t.Helper()

	// zap.NewNop keeps the failure log out of test output. The service logs the
	// enrichment error precisely because it is not returned, so it has to be
	// given a usable logger.
	return enrichment.NewService(repository, enricher, zap.NewNop())
}

func text(value string) pgtype.Text {
	return pgtype.Text{
		String: value,
		Valid:  true,
	}
}

func validText(value string) *string {
	return &value
}

// requireAppError asserts that err is an AppError with the given status and code.
func requireAppError(
	t *testing.T,
	err error,
	expectedStatus int,
	expectedCode string,
) {
	t.Helper()

	require.Error(t, err)

	appErr := apperror.FromError(err)
	require.Equal(t, expectedStatus, appErr.Status)
	require.Equal(t, expectedCode, appErr.Code)
}

func TestService_Enrich_Success(t *testing.T) {
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()
	rawURL := "https://example.com/articles/1"

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return(rawURL, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), rawURL).
		Return(enrichmentcore.Metadata{
			Title:       validText("An interesting article"),
			Platform:    validText("example"),
			Description: validText("A short summary"),
			ImageURL:    validText("https://example.com/og.png"),
		}, nil)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		DoAndReturn(
			func(
				_ context.Context,
				params enrichment.CompleteSavedItemEnrichmentParams,
			) (enrichment.SavedItem, error) {
				require.Equal(t, userID, params.UserID)
				require.Equal(t, savedItemID, params.SavedItemID)
				require.Equal(t, "An interesting article", *params.Title)
				require.Equal(t, "example", *params.Platform)
				require.Equal(t, "A short summary", *params.Description)
				require.Equal(t, "https://example.com/og.png", *params.ImageURL)

				return enrichment.SavedItem{
					ID:               savedItemID,
					UserID:           userID,
					Url:              rawURL,
					Title:            text("An interesting article"),
					Platform:         text("example"),
					Description:      text("A short summary"),
					ImageURL:         text("https://example.com/og.png"),
					EnrichmentStatus: "completed",
				}, nil
			},
		)

	result, err := newTestService(t, repository, enricher).
		Enrich(context.Background(), userID, savedItemID)

	require.NoError(t, err)
	require.Equal(t, savedItemID, result.SavedItem.ID)
	require.Equal(t, "completed", result.SavedItem.EnrichmentStatus)
	require.Equal(t, "An interesting article", result.SavedItem.Title.String)
}

func TestService_Enrich_PersistsOnlyWhatTheEnricherFound(t *testing.T) {
	// A page that exposed a title and nothing else. The absent fields must reach
	// the repository as nil rather than as empty strings, because nil is what
	// becomes SQL NULL.
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()
	rawURL := "https://example.com/bare"

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return(rawURL, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), rawURL).
		Return(enrichmentcore.Metadata{
			Title: validText("Only a title"),
		}, nil)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		DoAndReturn(
			func(
				_ context.Context,
				params enrichment.CompleteSavedItemEnrichmentParams,
			) (enrichment.SavedItem, error) {
				require.Equal(t, "Only a title", *params.Title)
				require.Nil(t, params.Platform)
				require.Nil(t, params.Description)
				require.Nil(t, params.ImageURL)

				return enrichment.SavedItem{
					ID:               savedItemID,
					Title:            text("Only a title"),
					EnrichmentStatus: "completed",
				}, nil
			},
		)

	result, err := newTestService(t, repository, enricher).
		Enrich(context.Background(), userID, savedItemID)

	require.NoError(t, err)
	require.Equal(t, "completed", result.SavedItem.EnrichmentStatus)
}

func TestService_Enrich_EmptyMetadataIsStillCompleted(t *testing.T) {
	// A page with no usable metadata is a successful enrichment with an empty
	// result, never a failure.
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()
	rawURL := "https://example.com/empty"

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return(rawURL, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), rawURL).
		Return(enrichmentcore.Metadata{}, nil)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Return(enrichment.SavedItem{
			ID:               savedItemID,
			EnrichmentStatus: "completed",
		}, nil)

	// A page with nothing to say must not take the failure path.
	repository.EXPECT().
		FailSavedItemEnrichment(gomock.Any(), gomock.Any(), gomock.Any()).
		Times(0)

	result, err := newTestService(t, repository, enricher).
		Enrich(context.Background(), userID, savedItemID)

	require.NoError(t, err)
	require.Equal(t, "completed", result.SavedItem.EnrichmentStatus)
}

func TestService_Enrich_FailureIsRecordedNotReturned(t *testing.T) {
	// The central behaviour: a page that cannot be read is recorded as failed and
	// reported as a successful operation, because the saved item is still valid.
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()
	rawURL := "https://example.com/gone"

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return(rawURL, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), rawURL).
		Return(
			enrichmentcore.Metadata{},
			enrichmentcore.ErrFetchFailed,
		)

	// A failed enrichment writes no metadata at all.
	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Times(0)

	repository.EXPECT().
		FailSavedItemEnrichment(gomock.Any(), userID, savedItemID).
		Return(enrichment.SavedItem{
			ID:               savedItemID,
			UserID:           userID,
			Url:              rawURL,
			EnrichmentStatus: "failed",
		}, nil)

	result, err := newTestService(t, repository, enricher).
		Enrich(context.Background(), userID, savedItemID)

	require.NoError(t, err)
	require.Equal(t, "failed", result.SavedItem.EnrichmentStatus)
}

func TestService_Enrich_DoesNotEnrichWhenTheItemIsNotFound(t *testing.T) {
	// Ownership is settled before anything is fetched, so a foreign or missing
	// item never causes an outbound request.
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return(
			"",
			apperror.NotFoundWith(
				enrichment.CodeSavedItemNotFound,
				"saved item not found",
				pgx.ErrNoRows,
			),
		)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().Enrich(gomock.Any(), gomock.Any()).Times(0)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Times(0)

	repository.EXPECT().
		FailSavedItemEnrichment(gomock.Any(), gomock.Any(), gomock.Any()).
		Times(0)

	_, err := newTestService(t, repository, enricher).
		Enrich(context.Background(), userID, savedItemID)

	requireAppError(t, err, 404, enrichment.CodeSavedItemNotFound)
}

func TestService_Enrich_ReturnsTheStatusWriteFailure(t *testing.T) {
	// If the item cannot even be marked failed, that is a database problem and is
	// worth returning. Reporting success would leave the item claiming to be
	// pending with no record of why.
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()
	rawURL := "https://example.com/gone"

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return(rawURL, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), rawURL).
		Return(
			enrichmentcore.Metadata{},
			enrichmentcore.ErrFetchFailed,
		)

	repository.EXPECT().
		FailSavedItemEnrichment(gomock.Any(), userID, savedItemID).
		Return(
			enrichment.SavedItem{},
			apperror.Internal(errors.New("connection reset")),
		)

	_, err := newTestService(t, repository, enricher).
		Enrich(context.Background(), userID, savedItemID)

	requireAppError(t, err, 500, apperror.CodeInternal)
}

func TestService_Enrich_ReturnsTheCompletionWriteFailure(t *testing.T) {
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()
	rawURL := "https://example.com/ok"

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return(rawURL, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), rawURL).
		Return(enrichmentcore.Metadata{
			Title: validText("Title"),
		}, nil)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Return(
			enrichment.SavedItem{},
			apperror.Internal(errors.New("deadlock")),
		)

	_, err := newTestService(t, repository, enricher).
		Enrich(context.Background(), userID, savedItemID)

	requireAppError(t, err, 500, apperror.CodeInternal)
}

func TestService_Enrich_PassesTheCallersContext(t *testing.T) {
	// The context the caller supplied must reach the enricher, so a deadline or a
	// cancellation from the request applies to the outbound fetch too.
	ctrl := gomock.NewController(t)

	userID := uuid.New()
	savedItemID := uuid.New()
	rawURL := "https://example.com/ctx"

	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("trace"), "abc")

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemURLForEnrichment(gomock.Any(), userID, savedItemID).
		Return(rawURL, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), rawURL).
		DoAndReturn(
			func(
				received context.Context,
				_ string,
			) (enrichmentcore.Metadata, error) {
				require.Equal(
					t,
					"abc",
					received.Value(contextKey("trace")),
				)

				return enrichmentcore.Metadata{}, nil
			},
		)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Return(enrichment.SavedItem{
			ID:               savedItemID,
			EnrichmentStatus: "completed",
		}, nil)

	_, err := newTestService(t, repository, enricher).
		Enrich(ctx, userID, savedItemID)

	require.NoError(t, err)
}
