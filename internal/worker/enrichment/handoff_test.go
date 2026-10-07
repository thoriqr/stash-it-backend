package enrichment_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	workerenrichment "github.com/thoriqr/stash-it-backend/internal/worker/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/worker/enrichment/mocks"
)

// These tests cover the enrichment -> organization handoff.
//
// The decision being protected is narrow and consequential: a successful
// enrichment that produced a platform schedules exactly one organization task,
// and nothing else schedules anything. There is no scan and no sweep to find
// items that look unorganized afterwards, so a task that is not created here is
// not created at all.

// handoffCase describes one enrichment outcome and whether it should schedule.
type handoffCase struct {
	name string

	// metadata is what the extractor reports.
	metadata enrichmentcore.Metadata

	// enrichErr is returned by the extractor instead of metadata, when non-nil.
	enrichErr error

	// shouldSchedule is whether an organization task is expected.
	shouldSchedule bool
}

// A successful enrichment with a platform is the only thing that schedules
// organization. Every other outcome leaves the item exactly as enrichment left
// it.
func TestService_EnrichSavedItem_OrganizationHandoff(t *testing.T) {
	cases := []handoffCase{
		{
			name: "a platform schedules organization",
			metadata: enrichmentcore.Metadata{
				Title:    validText("An interesting article"),
				Platform: validText("YouTube"),
			},
			shouldSchedule: true,
		},
		{
			name:           "an empty result schedules nothing",
			metadata:       enrichmentcore.Metadata{},
			shouldSchedule: false,
		},
		{
			name: "a title without a platform schedules nothing",
			metadata: enrichmentcore.Metadata{
				Title: validText("An interesting article"),
			},
			shouldSchedule: false,
		},
		{
			name: "a failed extraction schedules nothing",
			metadata: enrichmentcore.Metadata{
				Platform: validText("YouTube"),
			},
			enrichErr: fetchFailure(
				errors.New("dial tcp: lookup timed out"),
			),
			shouldSchedule: false,
		},
		{
			name: "a permanently failed extraction schedules nothing",
			metadata: enrichmentcore.Metadata{
				Platform: validText("YouTube"),
			},
			enrichErr:      contentFailure(errors.New("status 404")),
			shouldSchedule: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
				Return(tc.metadata, tc.enrichErr)

			organizer := mocks.NewMockSavedItemOrganizer(ctrl)

			if tc.enrichErr != nil {
				repository.EXPECT().
					FailSavedItemEnrichment(gomock.Any(), savedItemID).
					Return(nil)

				repository.EXPECT().
					CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
					Times(0)
			} else {
				repository.EXPECT().
					CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
					Return(
						workerenrichment.CompletedSavedItem{
							SavedItemID: savedItemID,
							UserID:      item.UserID,
							Platform:    tc.metadata.Platform,
						},
						nil,
					)
			}

			if tc.shouldSchedule {
				organizer.EXPECT().
					EnqueueSavedItemOrganization(
						gomock.Any(),
						savedItemID,
						item.UserID,
					).
					Return(nil)
			} else {
				organizer.EXPECT().
					EnqueueSavedItemOrganization(
						gomock.Any(),
						gomock.Any(),
						gomock.Any(),
					).
					Times(0)
			}

			err := newTestServiceWithOrganizer(
				repository,
				enricher,
				organizer,
			).EnrichSavedItem(context.Background(), savedItemID)

			// A failed extraction is still an error from the service's point of
			// view, because the queue has to decide whether to try the page again.
			if tc.enrichErr != nil {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
		})
	}
}

// The task is scheduled from the write's own committed result, not from the row
// loaded before the fetch. The owner the task carries is therefore the one the
// database confirmed, which is the only value a downstream owner-scoped decision
// can safely rely on.
func TestService_EnrichSavedItem_OrganizationCarriesTheCommittedOwner(t *testing.T) {
	ctrl := gomock.NewController(t)

	item := savedItem("https://example.com/articles/1")
	savedItemID := item.ID

	// The owner the enrichment write reports back differs from the one the load
	// saw. Only the committed value may reach the task.
	committedUserID := uuid.New()

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemForBackgroundEnrichment(gomock.Any(), savedItemID).
		Return(item, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), item.Url).
		Return(enrichmentcore.Metadata{Platform: validText("YouTube")}, nil)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Return(
			workerenrichment.CompletedSavedItem{
				SavedItemID: savedItemID,
				UserID:      committedUserID,
				Platform:    validText("YouTube"),
			},
			nil,
		)

	organizer := mocks.NewMockSavedItemOrganizer(ctrl)
	organizer.EXPECT().
		EnqueueSavedItemOrganization(gomock.Any(), savedItemID, committedUserID).
		Return(nil)

	require.NoError(
		t,
		newTestServiceWithOrganizer(repository, enricher, organizer).
			EnrichSavedItem(context.Background(), savedItemID),
	)
}

// A queue that cannot be reached does not fail the enrichment. The write has
// already committed and re-fetching the page to retry a secondary concern would
// overwrite metadata that is correct.
func TestService_EnrichSavedItem_OrganizationQueueFailureIsSwallowed(t *testing.T) {
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
		Return(enrichmentcore.Metadata{Platform: validText("YouTube")}, nil)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Return(
			workerenrichment.CompletedSavedItem{
				SavedItemID: savedItemID,
				UserID:      item.UserID,
				Platform:    validText("YouTube"),
			},
			nil,
		)

	organizer := mocks.NewMockSavedItemOrganizer(ctrl)
	organizer.EXPECT().
		EnqueueSavedItemOrganization(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(errors.New("dial tcp 127.0.0.1:6379: connect: connection refused"))

	require.NoError(
		t,
		newTestServiceWithOrganizer(repository, enricher, organizer).
			EnrichSavedItem(context.Background(), savedItemID),
		"a save that was enriched must not fail because its organization could not be queued",
	)
}

// A write that fails schedules nothing: the row was not updated, so there is no
// committed platform for a task to act on.
func TestService_EnrichSavedItem_NoOrganizationWhenTheWriteFails(t *testing.T) {
	ctrl := gomock.NewController(t)

	item := savedItem("https://example.com/articles/1")
	savedItemID := item.ID

	writeErr := errors.New("deadlock detected")

	repository := mocks.NewMockRepository(ctrl)
	repository.EXPECT().
		GetSavedItemForBackgroundEnrichment(gomock.Any(), savedItemID).
		Return(item, nil)

	enricher := mocks.NewMockMetadataEnricher(ctrl)
	enricher.EXPECT().
		Enrich(gomock.Any(), item.Url).
		Return(enrichmentcore.Metadata{Platform: validText("YouTube")}, nil)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Return(workerenrichment.CompletedSavedItem{}, writeErr)

	organizer := mocks.NewMockSavedItemOrganizer(ctrl)
	organizer.EXPECT().
		EnqueueSavedItemOrganization(gomock.Any(), gomock.Any(), gomock.Any()).
		Times(0)

	err := newTestServiceWithOrganizer(repository, enricher, organizer).
		EnrichSavedItem(context.Background(), savedItemID)

	require.ErrorIs(t, err, writeErr)
}

// A process that enriches without arranging for anything else still enriches. A
// nil organizer is how "this process schedules no follow-up work" is expressed.
func TestService_EnrichSavedItem_WorksWithoutAnOrganizer(t *testing.T) {
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
		Return(enrichmentcore.Metadata{Platform: validText("YouTube")}, nil)

	repository.EXPECT().
		CompleteSavedItemEnrichment(gomock.Any(), gomock.Any()).
		Return(
			workerenrichment.CompletedSavedItem{
				SavedItemID: savedItemID,
				UserID:      item.UserID,
				Platform:    validText("YouTube"),
			},
			nil,
		)

	require.NoError(
		t,
		newTestService(repository, enricher).
			EnrichSavedItem(context.Background(), savedItemID),
	)
}
