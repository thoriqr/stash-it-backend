package organization_test

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
	workerorganization "github.com/thoriqr/stash-it-backend/internal/worker/organization"
	"github.com/thoriqr/stash-it-backend/internal/worker/organization/mocks"
	"github.com/thoriqr/stash-it-backend/internal/worker/queue"
)

func newOrganizationTask(
	t *testing.T,
	savedItemID uuid.UUID,
	userID uuid.UUID,
) *asynq.Task {
	t.Helper()

	payload, err := json.Marshal(
		queue.NewOrganizeSavedItemPayload(savedItemID, userID),
	)
	require.NoError(t, err)

	return asynq.NewTask(queue.TaskTypeOrganizeSavedItem, payload)
}

func newTestHandler(service workerorganization.Service) *workerorganization.Handler {
	// The handler logs every terminal outcome, so it is given a logger rather than
	// left nil.
	return workerorganization.NewHandler(service, zap.NewNop())
}

func TestHandler_OrganizeSavedItem_Success(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()
	userID := uuid.New()

	service := mocks.NewMockService(ctrl)
	service.EXPECT().
		OrganizeSavedItem(gomock.Any(), savedItemID, userID).
		Return(nil)

	err := newTestHandler(service).OrganizeSavedItem(
		context.Background(),
		newOrganizationTask(t, savedItemID, userID),
	)

	require.NoError(t, err)
}

// Every no-op is a completed task, not a failure. Each of them is the expected
// shape of this task, and treating them as errors would spend the retry budget
// rediscovering a fact that cannot change.
func TestHandler_OrganizeSavedItem_NoOpsAreNotErrors(t *testing.T) {
	cases := []struct {
		name    string
		outcome workerorganization.Outcome
	}{
		{"no platform", workerorganization.OutcomeNoPlatform},
		{
			"enrichment not completed",
			workerorganization.OutcomeEnrichmentNotCompleted,
		},
		{
			"not in unsorted",
			workerorganization.OutcomeNotInUnsorted,
		},
		{
			"already organized",
			workerorganization.OutcomeAlreadyOrganized,
		},
		{"moved", workerorganization.OutcomeMoved},
		{
			"collection created",
			workerorganization.OutcomeCollectionCreated,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)

			savedItemID := uuid.New()
			userID := uuid.New()

			service := mocks.NewMockService(ctrl)
			service.EXPECT().
				OrganizeSavedItem(gomock.Any(), savedItemID, userID).
				Return(nil)

			err := newTestHandler(service).OrganizeSavedItem(
				context.Background(),
				newOrganizationTask(t, savedItemID, userID),
			)

			require.NoError(t, err)
			require.NotErrorIs(t, err, asynq.SkipRetry)
		})
	}
}

// A deleted item is a finished task. It is discarded rather than archived, which
// is what the enrichment worker does for the same situation and what keeps dead
// tasks out of a list someone has to read.
func TestHandler_OrganizeSavedItem_MissingItemIsDiscarded(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()
	userID := uuid.New()

	service := mocks.NewMockService(ctrl)
	service.EXPECT().
		OrganizeSavedItem(gomock.Any(), savedItemID, userID).
		Return(apperror.NotFoundWith(
			workerorganization.CodeSavedItemNotFound,
			"saved item not found",
			workerorganization.ErrSavedItemNotFound,
		))

	err := newTestHandler(service).OrganizeSavedItem(
		context.Background(),
		newOrganizationTask(t, savedItemID, userID),
	)

	require.NoError(
		t,
		err,
		"a task for a deleted item is finished, not failed",
	)
}

// An ownership mismatch cannot be repaired by trying again, and it must not be
// discarded silently: it would hide a bug that files one user's item into another
// user's collection. It is archived instead, so the stopped task is visible.
func TestHandler_OrganizeSavedItem_OwnershipMismatchIsArchived(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()
	userID := uuid.New()

	service := mocks.NewMockService(ctrl)
	service.EXPECT().
		OrganizeSavedItem(gomock.Any(), savedItemID, userID).
		Return(apperror.ConflictWith(
			workerorganization.CodeSavedItemOwnershipMismatch,
			"saved item does not belong to the task's user",
			workerorganization.ErrSavedItemOwnershipMismatch,
		))

	err := newTestHandler(service).OrganizeSavedItem(
		context.Background(),
		newOrganizationTask(t, savedItemID, userID),
	)

	require.ErrorIs(t, err, asynq.SkipRetry)
	require.ErrorIs(
		t,
		err,
		workerorganization.ErrSavedItemOwnershipMismatch,
	)
}

// A reserved collection name cannot become a target, so the task is archived
// rather than retried.
func TestHandler_OrganizeSavedItem_ReservedNameIsArchived(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()
	userID := uuid.New()

	service := mocks.NewMockService(ctrl)
	service.EXPECT().
		OrganizeSavedItem(gomock.Any(), savedItemID, userID).
		Return(apperror.ConflictWith(
			workerorganization.CodeCollectionNameReserved,
			"collection name is reserved",
			workerorganization.ErrCollectionNameTakenByReservedName,
		))

	err := newTestHandler(service).OrganizeSavedItem(
		context.Background(),
		newOrganizationTask(t, savedItemID, userID),
	)

	require.ErrorIs(t, err, asynq.SkipRetry)
}

// Anything else is a database problem and is left to the retry budget.
func TestHandler_OrganizeSavedItem_DatabaseFailureIsRetried(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()
	userID := uuid.New()

	databaseErr := apperror.Internal(errors.New("deadlock detected"))

	service := mocks.NewMockService(ctrl)
	service.EXPECT().
		OrganizeSavedItem(gomock.Any(), savedItemID, userID).
		Return(databaseErr)

	err := newTestHandler(service).OrganizeSavedItem(
		context.Background(),
		newOrganizationTask(t, savedItemID, userID),
	)

	require.Error(t, err)
	require.NotErrorIs(
		t,
		err,
		asynq.SkipRetry,
		"a database failure must be left to the retry budget",
	)
	require.ErrorIs(t, err, databaseErr)
}

// A payload that cannot be read will not become readable on a retry.
func TestHandler_OrganizeSavedItem_UnreadablePayload(t *testing.T) {
	ctrl := gomock.NewController(t)

	service := mocks.NewMockService(ctrl)
	service.EXPECT().
		OrganizeSavedItem(gomock.Any(), gomock.Any(), gomock.Any()).
		Times(0)

	err := newTestHandler(service).OrganizeSavedItem(
		context.Background(),
		asynq.NewTask(
			queue.TaskTypeOrganizeSavedItem,
			[]byte("not json at all"),
		),
	)

	require.ErrorIs(t, err, asynq.SkipRetry)
}

// Both halves of the payload are required. A task with no owner has nothing to
// verify the item against, and one with no item has nothing to organize.
func TestHandler_OrganizeSavedItem_IncompletePayload(t *testing.T) {
	cases := []struct {
		name    string
		payload queue.OrganizeSavedItemPayload
	}{
		{
			name:    "no ids at all",
			payload: queue.OrganizeSavedItemPayload{},
		},
		{
			name: "no owner",
			payload: queue.OrganizeSavedItemPayload{
				SavedItemID: uuid.New(),
			},
		},
		{
			name: "no saved item",
			payload: queue.OrganizeSavedItemPayload{
				UserID: uuid.New(),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)

			payload, err := json.Marshal(tc.payload)
			require.NoError(t, err)

			service := mocks.NewMockService(ctrl)
			service.EXPECT().
				OrganizeSavedItem(gomock.Any(), gomock.Any(), gomock.Any()).
				Times(0)

			err = newTestHandler(service).OrganizeSavedItem(
				context.Background(),
				asynq.NewTask(queue.TaskTypeOrganizeSavedItem, payload),
			)

			require.ErrorIs(t, err, asynq.SkipRetry)
		})
	}
}

// ProcessTask is what Asynq calls, and it must behave exactly as the named
// method does. Asserting both keeps the adapter from becoming a second behavior.
func TestHandler_ProcessTask(t *testing.T) {
	ctrl := gomock.NewController(t)

	savedItemID := uuid.New()
	userID := uuid.New()

	service := mocks.NewMockService(ctrl)
	service.EXPECT().
		OrganizeSavedItem(gomock.Any(), savedItemID, userID).
		Return(nil)

	require.NoError(
		t,
		newTestHandler(service).ProcessTask(
			context.Background(),
			newOrganizationTask(t, savedItemID, userID),
		),
	)
}

// The handler must satisfy Asynq's interface, which is the only way the queue can
// call it at all.
func TestHandler_ImplementsAsynqHandler(t *testing.T) {
	var handler any = workerorganization.NewHandler(nil, zap.NewNop())

	_, ok := handler.(asynq.Handler)

	require.True(t, ok, "handler must satisfy asynq.Handler")
}

// The payload carries the saved item and its owner, and nothing that could go
// stale: no platform and no collection. Both are re-read at execution time.
func TestHandler_PayloadCarriesOnlyTheIDs(t *testing.T) {
	payload := queue.NewOrganizeSavedItemPayload(uuid.New(), uuid.New())

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded map[string]any

	require.NoError(t, json.Unmarshal(encoded, &decoded))

	require.Len(t, decoded, 2)
	require.Contains(t, decoded, "saved_item_id")
	require.Contains(t, decoded, "user_id")
}
