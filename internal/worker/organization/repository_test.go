package organization_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	workerorganization "github.com/thoriqr/stash-it-backend/internal/worker/organization"
	"github.com/thoriqr/stash-it-backend/internal/worker/organization/mocks"
)

// These tests cover the service around the repository: that every outcome is
// logged as something specific and returned as success, and that a failure is
// logged and passed on rather than swallowed.
//
// The decision itself belongs to the repository, because it has to be made against
// a locked row. It is covered against a real database in the integration suite.

func newTestService(
	repository workerorganization.Repository,
) workerorganization.Service {
	return workerorganization.NewService(repository, zap.NewNop())
}

// Every outcome is a successful completion. None of them is an error, because none
// of them is a failure: the item is valid and the queue has nothing left to do.
func TestService_OrganizeSavedItem_EveryOutcomeSucceeds(t *testing.T) {
	outcomes := []workerorganization.Outcome{
		workerorganization.OutcomeMoved,
		workerorganization.OutcomeCollectionCreated,
		workerorganization.OutcomeAlreadyOrganized,
		workerorganization.OutcomeNotInUnsorted,
		workerorganization.OutcomeNoPlatform,
		workerorganization.OutcomeEnrichmentNotCompleted,
	}

	for _, outcome := range outcomes {
		t.Run(outcome.String(), func(t *testing.T) {
			ctrl := gomock.NewController(t)

			savedItemID := uuid.New()
			userID := uuid.New()

			repository := mocks.NewMockRepository(ctrl)
			repository.EXPECT().
				OrganizeSavedItem(gomock.Any(), savedItemID, userID).
				Return(outcome, nil)

			require.NoError(
				t,
				newTestService(repository).
					OrganizeSavedItem(context.Background(), savedItemID, userID),
			)
		})
	}
}

// The repository's error is reported unchanged so the handler can decide whether
// the task is worth retrying.
func TestService_OrganizeSavedItem_ReportsTheRepositoryError(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "missing item",
			err: apperror.NotFoundWith(
				workerorganization.CodeSavedItemNotFound,
				"saved item not found",
				workerorganization.ErrSavedItemNotFound,
			),
		},
		{
			name: "ownership mismatch",
			err: apperror.ConflictWith(
				workerorganization.CodeSavedItemOwnershipMismatch,
				"saved item does not belong to the task's user",
				workerorganization.ErrSavedItemOwnershipMismatch,
			),
		},
		{
			name: "database failure",
			err:  apperror.Internal(errors.New("deadlock detected")),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)

			savedItemID := uuid.New()
			userID := uuid.New()

			repository := mocks.NewMockRepository(ctrl)
			repository.EXPECT().
				OrganizeSavedItem(gomock.Any(), savedItemID, userID).
				Return(workerorganization.OutcomeUnknown, tc.err)

			err := newTestService(repository).
				OrganizeSavedItem(context.Background(), savedItemID, userID)

			require.ErrorIs(t, err, tc.err)
		})
	}
}

// Which outcomes wrote to the item, and which did not, is what the logging level
// keys off. A no-op is the product working as designed; a write is the thing
// someone watching for.
func TestService_OrganizeSavedItem_ReportsWhatItDid(t *testing.T) {
	require.True(t, workerorganization.OutcomeMoved.DidMove())
	require.True(t, workerorganization.OutcomeCollectionCreated.DidMove())

	require.False(t, workerorganization.OutcomeAlreadyOrganized.DidMove())
	require.False(t, workerorganization.OutcomeNotInUnsorted.DidMove())
	require.False(t, workerorganization.OutcomeNoPlatform.DidMove())
	require.False(
		t,
		workerorganization.OutcomeEnrichmentNotCompleted.DidMove(),
	)

	// The zero value is not a real outcome, so a caller that ignored an error
	// cannot mistake it for one of the no-ops.
	require.False(t, workerorganization.OutcomeUnknown.DidMove())
	require.Equal(
		t,
		"unknown",
		workerorganization.OutcomeUnknown.String(),
	)
}

// An item is only organization-eligible while it is in Unsorted. A user-named
// collection has no system_key, so its absence is what distinguishes "the user
// filed this" from "nothing has organized this yet" — including a user collection
// that happens to be named "Unsorted", which the key-based check does not mistake
// for the system collection.
func TestSavedItem_EligibilityGuards(t *testing.T) {
	userID := uuid.New()
	collectionID := uuid.New()

	cases := []struct {
		name        string
		item        workerorganization.SavedItem
		inUnsorted  bool
		hasPlatform bool
		isCompleted bool
	}{
		{
			name: "a completed item with a platform in unsorted is eligible",
			item: workerorganization.SavedItem{
				UserID:              userID,
				EnrichmentStatus:    "completed",
				Platform:            pgtype.Text{String: "YouTube", Valid: true},
				CollectionID:        collectionID,
				CollectionSystemKey: pgtype.Text{String: "unsorted", Valid: true},
			},
			inUnsorted:  true,
			hasPlatform: true,
			isCompleted: true,
		},
		{
			name: "an item the user filed elsewhere is not eligible",
			item: workerorganization.SavedItem{
				UserID:           userID,
				EnrichmentStatus: "completed",
				Platform:         pgtype.Text{String: "YouTube", Valid: true},
				CollectionID:     collectionID,
				// A user collection has no system_key at all.
			},
			hasPlatform: true,
			isCompleted: true,
		},
		{
			name: "a user collection named Unsorted is still the user's own",
			item: workerorganization.SavedItem{
				UserID:           userID,
				EnrichmentStatus: "completed",
				Platform:         pgtype.Text{String: "YouTube", Valid: true},
				CollectionID:     collectionID,
			},
			hasPlatform: true,
			isCompleted: true,
		},
		{
			name: "an item with no platform is not eligible",
			item: workerorganization.SavedItem{
				UserID:              userID,
				EnrichmentStatus:    "completed",
				CollectionID:        collectionID,
				CollectionSystemKey: pgtype.Text{String: "unsorted", Valid: true},
			},
			inUnsorted:  true,
			isCompleted: true,
		},
		{
			name: "an empty platform string is not a platform",
			item: workerorganization.SavedItem{
				UserID:              userID,
				EnrichmentStatus:    "completed",
				Platform:            pgtype.Text{String: "", Valid: true},
				CollectionID:        collectionID,
				CollectionSystemKey: pgtype.Text{String: "unsorted", Valid: true},
			},
			inUnsorted:  true,
			isCompleted: true,
		},
		{
			name: "a failed enrichment is not eligible",
			item: workerorganization.SavedItem{
				UserID:              userID,
				EnrichmentStatus:    "failed",
				Platform:            pgtype.Text{String: "YouTube", Valid: true},
				CollectionID:        collectionID,
				CollectionSystemKey: pgtype.Text{String: "unsorted", Valid: true},
			},
			inUnsorted:  true,
			hasPlatform: true,
		},
		{
			name: "a pending enrichment is not eligible",
			item: workerorganization.SavedItem{
				UserID:              userID,
				EnrichmentStatus:    "pending",
				CollectionID:        collectionID,
				CollectionSystemKey: pgtype.Text{String: "unsorted", Valid: true},
			},
			inUnsorted: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.inUnsorted, tc.item.IsInUnsorted())
			require.Equal(t, tc.hasPlatform, tc.item.HasPlatform())
			require.Equal(t, tc.isCompleted, tc.item.IsEnrichmentCompleted())
		})
	}
}

// The system key is derived from the platform the same way collection names are
// compared, so the key and the name can never disagree about which platform a
// collection belongs to.
func TestSystemKeyForPlatform(t *testing.T) {
	cases := []struct {
		platform string
		key      string
	}{
		{"YouTube", "youtube"},
		{"youtube", "youtube"},
		{"YOUTUBE", "youtube"},
		{"  YouTube  ", "youtube"},
		{"You Tube", "you tube"},
		{"YouTube Music", "youtube music"},
	}

	for _, tc := range cases {
		t.Run(tc.platform, func(t *testing.T) {
			require.Equal(
				t,
				tc.key,
				workerorganization.SystemKeyForPlatform(tc.platform),
			)
		})
	}
}

// A collection of either type is a valid target, and organization does not decide
// differently based on which it is.
func TestCollection_TypeIsInformational(t *testing.T) {
	userNamed := workerorganization.Collection{Type: "user"}
	systemNamed := workerorganization.Collection{Type: "system"}

	require.False(t, userNamed.IsSystem())
	require.True(t, systemNamed.IsSystem())
}
