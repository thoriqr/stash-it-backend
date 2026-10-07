package integration_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/logger"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	workerenrichmentdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/worker_enrichment/generated"
	workerenrichment "github.com/thoriqr/stash-it-backend/internal/worker/enrichment"
	enrichmentdb "github.com/thoriqr/stash-it-backend/internal/worker/enrichment/generated"
)

// These tests drive background enrichment against a real database: the worker's
// own repository, its worker-scoped sqlc target, the baseline schema, and the
// real statements.
//
// Only the extraction layer is replaced, for the same reason the synchronous
// enrichment tests replace it. The real one is built around the guarded outbound
// HTTP client, and that client correctly refuses loopback, so a test could not
// reach an httptest server through it. What belongs under test here is what the
// worker writes, not what the extractor finds.

func truncateWorkerEnrichmentData(t *testing.T) {
	t.Helper()

	require.NoError(
		t,
		workerenrichmentdbtest.New(testPool).
			TruncateWorkerEnrichmentData(context.Background()),
	)
}

func createWorkerEnrichmentUser(t *testing.T, email string) uuid.UUID {
	t.Helper()

	userID, err := workerenrichmentdbtest.New(testPool).
		CreateWorkerEnrichmentUser(
			context.Background(),
			workerenrichmentdbtest.CreateWorkerEnrichmentUserParams{
				Email:       email,
				DisplayName: "Worker Enrichment Test User",
			},
		)

	require.NoError(t, err)

	return userID
}

func createUnsortedForWorkerEnrichment(
	t *testing.T,
	userID uuid.UUID,
) uuid.UUID {
	t.Helper()

	collectionID, err := workerenrichmentdbtest.New(testPool).
		CreateUnsortedCollectionForWorkerEnrichment(
			context.Background(),
			userID,
		)

	require.NoError(t, err)

	return collectionID
}

func createUserCollectionForWorkerEnrichment(
	t *testing.T,
	userID uuid.UUID,
	name string,
) uuid.UUID {
	t.Helper()

	collectionID, err := workerenrichmentdbtest.New(testPool).
		CreateTestUserCollectionForWorkerEnrichment(
			context.Background(),
			workerenrichmentdbtest.CreateTestUserCollectionForWorkerEnrichmentParams{
				UserID: userID,
				Name:   name,
			},
		)

	require.NoError(t, err)

	return collectionID
}

func createPendingSavedItemForWorkerEnrichment(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
	rawURL string,
) uuid.UUID {
	t.Helper()

	savedItemID, err := workerenrichmentdbtest.New(testPool).
		CreatePendingSavedItemForWorkerEnrichment(
			context.Background(),
			workerenrichmentdbtest.CreatePendingSavedItemForWorkerEnrichmentParams{
				UserID:       userID,
				Url:          rawURL,
				Domain:       testText("example.com"),
				CollectionID: collectionID,
			},
		)

	require.NoError(t, err)

	return savedItemID
}

func workerEnrichmentState(
	t *testing.T,
	savedItemID uuid.UUID,
) workerenrichmentdbtest.GetSavedItemWorkerEnrichmentStateRow {
	t.Helper()

	state, err := workerenrichmentdbtest.New(testPool).
		GetSavedItemWorkerEnrichmentState(
			context.Background(),
			savedItemID,
		)

	require.NoError(t, err)

	return state
}

// newRealWorkerService builds the worker service against the shared test pool,
// with a caller-supplied enricher and no organizer.
//
// Organization is asserted on its own in the organization tests, and its absence
// here is what keeps these tests about enrichment alone: a test that cared about
// the handoff would use newRealWorkerServiceWithOrganizer instead.
func newRealWorkerService(
	t *testing.T,
	enricher workerenrichment.MetadataEnricher,
) workerenrichment.Service {
	t.Helper()

	return newRealWorkerServiceWithOrganizer(t, enricher, nil)
}

// newRealWorkerServiceWithOrganizer builds the worker service against the shared
// test pool with a caller-supplied enricher and organizer.
func newRealWorkerServiceWithOrganizer(
	t *testing.T,
	enricher workerenrichment.MetadataEnricher,
	organizer workerenrichment.SavedItemOrganizer,
) workerenrichment.Service {
	t.Helper()

	log, err := logger.New("development")
	require.NoError(t, err)

	return workerenrichment.NewService(
		workerenrichment.NewRepository(enrichmentdb.New(testPool)),
		enricher,
		organizer,
		log,
	)
}

// A successful background enrichment writes the current metadata result, marks
// the item completed, and stamps last_enriched_at.
func TestWorkerEnrichment_CompletesAndWritesMetadata(t *testing.T) {
	truncateWorkerEnrichmentData(t)

	userID := createWorkerEnrichmentUser(t, "worker-complete@example.com")
	collectionID := createUnsortedForWorkerEnrichment(t, userID)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/articles/1",
	)

	before := workerEnrichmentState(t, savedItemID)

	require.Equal(t, "pending", before.EnrichmentStatus)
	require.False(t, before.LastEnrichedAt.Valid)

	title := "An interesting article"
	platform := "example"
	description := "A short summary"
	imageURL := "https://example.com/og.png"

	enricher := &testutil.FakeEnricher{
		Metadata: enrichmentcore.Metadata{
			Title:       &title,
			Platform:    &platform,
			Description: &description,
			ImageURL:    &imageURL,
		},
	}

	require.NoError(
		t,
		newRealWorkerService(t, enricher).
			EnrichSavedItem(context.Background(), savedItemID),
	)

	after := workerEnrichmentState(t, savedItemID)

	require.Equal(t, "completed", after.EnrichmentStatus)
	require.Equal(t, title, after.Title.String)
	require.True(t, after.Title.Valid)
	require.Equal(t, platform, after.Platform.String)
	require.Equal(t, description, after.Description.String)
	require.Equal(t, imageURL, after.ImageUrl.String)
	require.True(t, after.LastEnrichedAt.Valid)

	require.Equal(
		t,
		[]string{"https://example.com/articles/1"},
		enricher.RequestedURLs(),
		"the worker must fetch the URL stored on the row",
	)
}

// A page that exposes nothing is a successful enrichment. The item is completed
// and every metadata column is NULL, so no stale value survives.
func TestWorkerEnrichment_EmptyMetadataIsCompletedNotFailed(t *testing.T) {
	truncateWorkerEnrichmentData(t)

	userID := createWorkerEnrichmentUser(t, "worker-empty@example.com")
	collectionID := createUnsortedForWorkerEnrichment(t, userID)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/silent",
	)

	require.NoError(
		t,
		newRealWorkerService(t, &testutil.FakeEnricher{}).
			EnrichSavedItem(context.Background(), savedItemID),
	)

	after := workerEnrichmentState(t, savedItemID)

	require.Equal(t, "completed", after.EnrichmentStatus)
	require.False(t, after.Title.Valid)
	require.False(t, after.Platform.Valid)
	require.False(t, after.Description.Valid)
	require.False(t, after.ImageUrl.Valid)
	require.True(t, after.LastEnrichedAt.Valid)
}

// A re-enrichment that finds nothing clears the metadata a previous enrichment
// stored, rather than leaving stale values behind.
func TestWorkerEnrichment_RepeatClearsStaleMetadata(t *testing.T) {
	truncateWorkerEnrichmentData(t)

	userID := createWorkerEnrichmentUser(t, "worker-repeat@example.com")
	collectionID := createUnsortedForWorkerEnrichment(t, userID)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/changes",
	)

	title := "An interesting article"

	require.NoError(
		t,
		newRealWorkerService(t, &testutil.FakeEnricher{
			Metadata: enrichmentcore.Metadata{Title: &title},
		}).EnrichSavedItem(context.Background(), savedItemID),
	)

	require.True(t, workerEnrichmentState(t, savedItemID).Title.Valid)

	// The second attempt finds nothing at all, which is an ordinary outcome and
	// not a failure, so the item ends up completed with no title.
	require.NoError(
		t,
		newRealWorkerService(t, &testutil.FakeEnricher{}).
			EnrichSavedItem(context.Background(), savedItemID),
	)

	after := workerEnrichmentState(t, savedItemID)

	require.Equal(t, "completed", after.EnrichmentStatus)
	require.False(
		t,
		after.Title.Valid,
		"stale metadata must not survive an enrichment that found nothing",
	)
}

// A failed enrichment marks the item failed, preserves the metadata it already
// had, and leaves last_enriched_at alone.
func TestWorkerEnrichment_FailurePreservesMetadataAndTimestamp(t *testing.T) {
	truncateWorkerEnrichmentData(t)

	userID := createWorkerEnrichmentUser(t, "worker-failure@example.com")
	collectionID := createUnsortedForWorkerEnrichment(t, userID)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/flaky",
	)

	title := "An interesting article"
	imageURL := "https://example.com/og.png"

	require.NoError(
		t,
		newRealWorkerService(t, &testutil.FakeEnricher{
			Metadata: enrichmentcore.Metadata{
				Title:    &title,
				ImageURL: &imageURL,
			},
		}).EnrichSavedItem(context.Background(), savedItemID),
	)

	completed := workerEnrichmentState(t, savedItemID)

	require.Equal(t, "completed", completed.EnrichmentStatus)
	require.True(t, completed.LastEnrichedAt.Valid)

	pageErr := &enrichmentcore.Failure{
		Kind: enrichmentcore.FailureContent,
		Err:  errors.New("page returned an unexpected status: status 500"),
	}

	err := newRealWorkerService(t, &testutil.FakeEnricher{Err: pageErr}).
		EnrichSavedItem(context.Background(), savedItemID)

	// The failure is reported so the queue can decide about retries, and it keeps
	// the classification the extraction package attached.
	require.Error(t, err)
	require.Equal(
		t,
		enrichmentcore.FailureContent,
		enrichmentcore.Kind(err),
	)

	after := workerEnrichmentState(t, savedItemID)

	require.Equal(t, "failed", after.EnrichmentStatus)
	require.Equal(
		t,
		title,
		after.Title.String,
		"a failed attempt must not clear metadata the item already had",
	)
	require.Equal(t, imageURL, after.ImageUrl.String)

	require.Equal(
		t,
		completed.LastEnrichedAt.Time,
		after.LastEnrichedAt.Time,
		"a failed attempt is not a moment at which metadata was refreshed",
	)
}

// Enrichment never organizes. An item filed in a user collection is still there
// after background enrichment, completed or failed.
func TestWorkerEnrichment_NeverMovesCollection(t *testing.T) {
	truncateWorkerEnrichmentData(t)

	userID := createWorkerEnrichmentUser(t, "worker-collection@example.com")
	unsortedID := createUnsortedForWorkerEnrichment(t, userID)
	userCollectionID := createUserCollectionForWorkerEnrichment(
		t,
		userID,
		"Reading",
	)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		userCollectionID,
		"https://example.com/filed",
	)

	title := "A filed article"

	require.NoError(
		t,
		newRealWorkerService(t, &testutil.FakeEnricher{
			Metadata: enrichmentcore.Metadata{Title: &title},
		}).EnrichSavedItem(context.Background(), savedItemID),
	)

	require.Equal(
		t,
		userCollectionID,
		workerEnrichmentState(t, savedItemID).CollectionID,
	)

	// A failed attempt must not move it either.
	require.Error(
		t,
		newRealWorkerService(t, &testutil.FakeEnricher{
			Err: &enrichmentcore.Failure{
				Kind: enrichmentcore.FailureFetch,
				Err:  errors.New("dial tcp: i/o timeout"),
			},
		}).EnrichSavedItem(context.Background(), savedItemID),
	)

	state := workerEnrichmentState(t, savedItemID)

	require.Equal(t, "failed", state.EnrichmentStatus)
	require.Equal(t, userCollectionID, state.CollectionID)
	require.NotEqual(t, unsortedID, state.CollectionID)
}

// A task for a deleted item is reported with the sentinel the handler discards
// on, and no outbound request is attempted for a row that does not exist.
func TestWorkerEnrichment_MissingItemReportsTheDiscardSentinel(t *testing.T) {
	truncateWorkerEnrichmentData(t)

	userID := createWorkerEnrichmentUser(t, "worker-deleted@example.com")
	collectionID := createUnsortedForWorkerEnrichment(t, userID)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/deleted",
	)

	require.NoError(
		t,
		workerenrichmentdbtest.New(testPool).
			DeleteSavedItemForWorkerEnrichment(
				context.Background(),
				savedItemID,
			),
	)

	enricher := &testutil.FakeEnricher{}

	err := newRealWorkerService(t, enricher).
		EnrichSavedItem(context.Background(), savedItemID)

	require.ErrorIs(t, err, workerenrichment.ErrSavedItemNotFound)
	require.Equal(
		t,
		http.StatusNotFound,
		apperror.FromError(err).Status,
	)
	require.Equal(
		t,
		workerenrichment.CodeSavedItemNotFound,
		apperror.FromError(err).Code,
	)
	require.Empty(
		t,
		enricher.RequestedURLs(),
		"a deleted item must not trigger an outbound request",
	)
}

// Enrichment is repeatable, so running the same task twice must leave the row in
// the same state both times rather than in some half-finished one.
func TestWorkerEnrichment_RunningTwiceIsSafe(t *testing.T) {
	truncateWorkerEnrichmentData(t)

	userID := createWorkerEnrichmentUser(t, "worker-twice@example.com")
	collectionID := createUnsortedForWorkerEnrichment(t, userID)

	savedItemID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/twice",
	)

	title := "A stable title"

	service := newRealWorkerService(t, &testutil.FakeEnricher{
		Metadata: enrichmentcore.Metadata{Title: &title},
	})

	ctx := context.Background()

	require.NoError(t, service.EnrichSavedItem(ctx, savedItemID))

	first := workerEnrichmentState(t, savedItemID)

	require.NoError(t, service.EnrichSavedItem(ctx, savedItemID))

	second := workerEnrichmentState(t, savedItemID)

	require.Equal(t, first.EnrichmentStatus, second.EnrichmentStatus)
	require.Equal(t, first.Title.String, second.Title.String)
	require.Equal(t, first.Platform.String, second.Platform.String)
	require.Equal(t, first.Description.String, second.Description.String)
	require.Equal(t, first.ImageUrl.String, second.ImageUrl.String)
	require.Equal(t, first.CollectionID, second.CollectionID)
}

// The worker's read is not scoped by user, because a task names the item it is
// about and there is no caller to mislead. The row it loads is still the item the
// task named.
func TestWorkerEnrichment_LoadsTheRowItWasGiven(t *testing.T) {
	truncateWorkerEnrichmentData(t)

	userID := createWorkerEnrichmentUser(t, "worker-scope@example.com")
	collectionID := createUnsortedForWorkerEnrichment(t, userID)

	firstID := createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/first",
	)

	createPendingSavedItemForWorkerEnrichment(
		t,
		userID,
		collectionID,
		"https://example.com/second",
	)

	enricher := &testutil.FakeEnricher{}

	require.NoError(
		t,
		newRealWorkerService(t, enricher).
			EnrichSavedItem(context.Background(), firstID),
	)

	require.Equal(
		t,
		[]string{"https://example.com/first"},
		enricher.RequestedURLs(),
		"only the item named by the task may be enriched",
	)
}
