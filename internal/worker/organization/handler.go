package organization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/worker/queue"
)

// Handler consumes organization tasks.
//
// It is the only Asynq-aware part of this feature, for the same reason the
// enrichment worker's handler is: everything below it — the service, the
// repository — knows nothing about queues, which is what lets a task be produced
// by one process and consumed by another without either owning the other.
type Handler struct {
	service Service
	log     *zap.Logger
}

func NewHandler(service Service, log *zap.Logger) *Handler {
	return &Handler{
		service: service,
		log:     log,
	}
}

// ProcessTask satisfies asynq.Handler and forwards to OrganizeSavedItem.
//
// It exists so the handler can be registered with Asynq without naming its method
// after the library: OrganizeSavedItem says what the task does, and this says
// only that the queue is allowed to call it.
func (h *Handler) ProcessTask(
	ctx context.Context,
	task *asynq.Task,
) error {
	return h.OrganizeSavedItem(ctx, task)
}

// OrganizeSavedItem runs one automatic organization task.
//
// What this decides is not whether to organize, but what a failure means for the
// queue. The outcomes are distinct and none of them is the default:
//
//   - The item is gone. The task succeeds and is discarded, exactly as the
//     enrichment worker discards a task for a deleted item. Nothing on the far side
//     of a retry could bring it back.
//   - The task names somebody else's item, or names a collection that cannot be a
//     target. The item is never touched and the task is archived: these are
//     failures rather than finished work, and a person has to look at them.
//   - Anything else. Asynq retries within the bounded budget, which covers a
//     transient database problem such as lock contention.
//
// A no-op decided inside the service — no platform, enrichment incomplete, or the
// user already filed the item — arrives here as a nil error and needs no
// handling at all. That is why those cases are not errors at all: they are the
// task being finished.
//
// The handler is safe to run twice for the same item. A duplicate delivery finds
// the item already in the target collection and does nothing, so it creates no
// second collection and does not touch updated_at.
func (h *Handler) OrganizeSavedItem(
	ctx context.Context,
	task *asynq.Task,
) error {
	var payload queue.OrganizeSavedItemPayload

	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		// A payload that cannot be read will not become readable on a retry, and
		// there is no id in it to work from, which is the argument for not trying
		// again.
		h.log.Error(
			"discarding organization task with unreadable payload",
			zap.String("task_type", task.Type()),
			zap.String("task_id", taskID(ctx)),
			zap.Error(err),
		)

		return fmt.Errorf(
			"reading organization task payload: %w",
			errors.Join(asynq.SkipRetry, err),
		)
	}

	if payload.SavedItemID == uuid.Nil || payload.UserID == uuid.Nil {
		h.log.Error(
			"discarding organization task without a saved item id and owner",
			zap.String("task_type", task.Type()),
			zap.String("task_id", taskID(ctx)),
		)

		return fmt.Errorf(
			"organization task carried no saved item id and owner: %w",
			asynq.SkipRetry,
		)
	}

	err := h.service.OrganizeSavedItem(
		ctx,
		payload.SavedItemID,
		payload.UserID,
	)
	if err == nil {
		return nil
	}

	// A deleted item is a finished task, not a failed one, and returning nil is what
	// discards it. Nothing on the far side of a retry could bring a deleted item
	// back, and there is no row left to fix, so archiving it would put a dead task
	// in a list someone has to read for no reason. This is the enrichment worker's
	// handling of the same situation, kept identical deliberately.
	if errors.Is(err, ErrSavedItemNotFound) {
		h.log.Info(
			"discarding organization task for a deleted saved item",
			zap.String("saved_item_id", payload.SavedItemID.String()),
			zap.String("user_id", payload.UserID.String()),
			zap.String("task_id", taskID(ctx)),
		)

		return nil
	}

	// The other two are failures of the job rather than finished work: a task naming
	// somebody else's item, or a platform whose name cannot be a target. Neither can
	// be repaired by trying again, so Asynq is told to stop, and they are archived
	// rather than discarded because a person has to look at them. An archived task
	// is the visible record; discarding it would hide a bug that files one user's
	// item into another user's collection.
	if isIntegrityFailure(err) {
		h.log.Error(
			"organization task cannot be applied",
			zap.String("saved_item_id", payload.SavedItemID.String()),
			zap.String("user_id", payload.UserID.String()),
			zap.String("task_id", taskID(ctx)),
			zap.String("reason", integrityReason(err)),
			zap.Error(err),
		)

		return fmt.Errorf(
			"organization task cannot apply (%s): %w",
			integrityReason(err),
			errors.Join(asynq.SkipRetry, err),
		)
	}

	// Everything else is a database problem. It is logged at Warn rather than Error
	// because it is expected to clear on its own: the retry budget exists for
	// exactly this, and an item whose organization keeps failing is still a valid
	// saved item in a valid collection.
	h.log.Warn(
		"automatic organization attempt failed, will retry",
		zap.String("saved_item_id", payload.SavedItemID.String()),
		zap.String("user_id", payload.UserID.String()),
		zap.String("task_id", taskID(ctx)),
		zap.Int("retry", retryCount(ctx)),
		zap.Error(err),
	)

	return err
}

// isIntegrityFailure reports whether the task cannot be applied and no further
// attempt could change that.
func isIntegrityFailure(err error) bool {
	return errors.Is(err, ErrSavedItemOwnershipMismatch) ||
		errors.Is(err, ErrCollectionNameTakenByReservedName)
}

// integrityReason names which integrity condition it was, for the log line.
func integrityReason(err error) string {
	switch {
	case errors.Is(err, ErrSavedItemOwnershipMismatch):
		return "ownership mismatch"
	case errors.Is(err, ErrCollectionNameTakenByReservedName):
		return "collection name reserved"
	default:
		return "unknown"
	}
}

// taskID reads Asynq's task id from the context for logging.
//
// It is absent outside a processor, which is the case in tests, so an empty id is
// normal rather than a missing value.
func taskID(ctx context.Context) string {
	id, _ := asynq.GetTaskID(ctx)

	return id
}

// retryCount reads how many times this task has already been retried, for
// logging. Absent outside a processor, which is the case in tests.
func retryCount(ctx context.Context) int {
	count, _ := asynq.GetRetryCount(ctx)

	return count
}
