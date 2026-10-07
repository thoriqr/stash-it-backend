package enrichment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/worker/queue"
)

// Handler consumes enrichment tasks.
//
// It is the only Asynq-aware part of the feature. Everything below it — the
// service, the repository, the enrichment capability — knows nothing about
// queues, which is what lets the same records be produced by a save and consumed
// by a worker without either end owning the other.
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

// ProcessTask satisfies asynq.Handler and forwards to EnrichSavedItem.
//
// It exists so the handler can be registered with Asynq without naming its method
// after the library: EnrichSavedItem says what the task does, and this says only
// that the queue is allowed to call it.
func (h *Handler) ProcessTask(
	ctx context.Context,
	task *asynq.Task,
) error {
	return h.EnrichSavedItem(ctx, task)
}

// EnrichSavedItem runs one background enrichment task.
//
// What this function decides is not whether to enrich, but what a failure means
// for the queue. The three outcomes are distinct and none of them is the default:
//
//   - The item is gone. The task succeeds. There is nothing on the far side of a
//     retry that could bring a deleted item back, so retrying would spend queue
//     space and network on a certainty.
//   - The enrichment failed permanently. The item is already recorded as failed,
//     and the page is not going to become readable by being asked again, so Asynq
//     is told to stop retrying.
//   - Anything else. Asynq retries with the bounded backoff the queue package
//     defines, which covers a transient network or server problem and also a
//     database one.
//
// The classification is read from enrichment.Kind, which is the classification
// the enrichment capability already attaches to every error it returns. This does
// not define a second opinion about what a page error means; it maps one existing
// classification onto a queue decision, which is the only thing the worker adds.
//
// What is persisted is unaffected by any of it. Every attempt records the same
// thing on the item: `completed` when extraction succeeded, `failed` when it did
// not. There is no retrying or processing state, so a retried attempt is not a
// distinguishable state of the row and never becomes one.
//
// The handler is safe to run twice for the same item. Enrichment is repeatable by
// design, so a duplicate delivery, a redelivery after a lost ack, or a user
// asking again all write the same columns from the same source of truth and
// cannot leave a half-finished state behind: each statement is one unconditional
// write.
func (h *Handler) EnrichSavedItem(
	ctx context.Context,
	task *asynq.Task,
) error {
	var payload queue.EnrichSavedItemPayload

	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		// A payload that cannot be read will not become readable on a retry. There
		// is no id in it, so nothing can be logged about which item it was for,
		// which is the argument for not trying again: this task cannot ever do the
		// work it was created for.
		h.log.Error(
			"discarding enrichment task with unreadable payload",
			zap.String("task_type", task.Type()),
			zap.String("task_id", taskID(ctx)),
			zap.Error(err),
		)

		return fmt.Errorf(
			"reading enrichment task payload: %w",
			errors.Join(asynq.SkipRetry, err),
		)
	}

	if payload.SavedItemID == uuid.Nil {
		h.log.Error(
			"discarding enrichment task without a saved item id",
			zap.String("task_type", task.Type()),
			zap.String("task_id", taskID(ctx)),
		)

		return fmt.Errorf(
			"enrichment task carried no saved item id: %w",
			asynq.SkipRetry,
		)
	}

	err := h.service.EnrichSavedItem(ctx, payload.SavedItemID)
	if err == nil {
		h.log.Info(
			"background enrichment completed",
			zap.String("saved_item_id", payload.SavedItemID.String()),
			zap.String("task_id", taskID(ctx)),
		)

		return nil
	}

	// A deleted item is a finished task, not a failed one. Returning nil is what
	// discards it: there is no state on the row left to fix, so a retry has
	// nothing to do but fail the same way.
	if errors.Is(err, ErrSavedItemNotFound) {
		h.log.Info(
			"discarding enrichment task for a deleted saved item",
			zap.String("saved_item_id", payload.SavedItemID.String()),
			zap.String("task_id", taskID(ctx)),
		)

		return nil
	}

	kind := enrichment.Kind(err)

	if isPermanent(kind) {
		h.log.Warn(
			"background enrichment failed permanently, not retrying",
			zap.String("saved_item_id", payload.SavedItemID.String()),
			zap.String("task_id", taskID(ctx)),
			zap.String("kind", string(kind)),
			zap.Error(err),
		)

		// The item is already recorded as failed, which is the durable part of this
		// outcome, so the task itself has nothing left to do.
		return fmt.Errorf(
			"enrichment failed permanently (%s): %w",
			kind,
			errors.Join(asynq.SkipRetry, err),
		)
	}

	h.log.Warn(
		"background enrichment attempt failed, will retry",
		zap.String("saved_item_id", payload.SavedItemID.String()),
		zap.String("task_id", taskID(ctx)),
		zap.String("kind", string(kind)),
		zap.Int("retry", retryCount(ctx)),
		zap.Error(err),
	)

	return err
}

// isPermanent reports whether another attempt could produce a different result.
//
// The two permanent kinds are a page that was reached and cannot be used, and a
// page that was reached and could not be understood. Neither changes when the
// same request is made again.
//
// The fetch kind is deliberately not treated as permanent, and that includes the
// statuses an origin uses to say "not right now": a 429 and a 503 arrive from
// the extraction package classified as fetch failures precisely because a later
// attempt can succeed. It also includes an error that carries no classification
// at all, which is what a database failure looks like here and which Asynq should
// retry.
//
// Retrying is bounded by the queue's own budget and its capped backoff, so
// treating a transient failure as retryable cannot loop: once the budget is
// exhausted Asynq archives the task, and the item already carries the durable
// record of the failure.
func isPermanent(kind enrichment.FailureKind) bool {
	switch kind {
	case enrichment.FailureContent, enrichment.FailureParse:
		return true
	case enrichment.FailureFetch:
		return false
	default:
		return false
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
