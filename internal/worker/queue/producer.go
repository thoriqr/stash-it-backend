package queue

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// Producer enqueues background work.
//
// It is deliberately the whole of the queue surface the API process needs.
// Enqueueing is a write of one small task; consuming is the worker's own
// business and belongs to the worker binary. Holding the task type and the
// payload here, next to the producer, is what keeps the two sides of the
// contract from drifting: they are two packages in two binaries that only agree
// through a string Asynq matches at runtime, so there has to be exactly one
// definition of both.
//
// The package imports no Fiber, nothing from internal/api, and no persistence.
// internal/api/saved_item depends on a narrow interface of its own rather than
// on this one, so the dependency runs from the composition root inward and not
// between two features.
type Producer struct {
	client *asynq.Client
}

// NewProducer builds a Producer over an existing Redis connection.
//
// The connection is taken as an argument rather than parsed from a URL here
// because the binaries already own that step, and because sharing one client
// means one pool, one ping and one Close for the process.
//
// Asynq does not close a client it did not create, so the caller keeps ownership
// of redisClient and closes it.
func NewProducer(redisClient redis.UniversalClient) *Producer {
	return &Producer{
		client: asynq.NewClientFromRedisClient(redisClient),
	}
}

// EnqueueSavedItemEnrichment queues background metadata enrichment for one
// saved item.
//
// The returned error is the caller's to decide on, and the one caller does treat
// it as recoverable: the saved item is already committed when this runs, so a
// queue that cannot be reached leaves the item exactly as valid as it was, with
// its enrichment columns still pending.
//
// No unique key is set on the task. Enrichment is repeatable by design, and a
// user is allowed to ask for the same item again; deduplicating the queue would
// make a second request silently depend on the first one's timing.
func (p *Producer) EnqueueSavedItemEnrichment(
	ctx context.Context,
	savedItemID uuid.UUID,
) error {
	payload, err := json.Marshal(NewEnrichSavedItemPayload(savedItemID))
	if err != nil {
		return fmt.Errorf("marshalling enrichment task payload: %w", err)
	}

	task := asynq.NewTask(TaskTypeEnrichSavedItem, payload)

	_, err = p.client.EnqueueContext(
		ctx,
		task,
		asynq.Queue(QueueEnrichment),
		asynq.Timeout(EnrichmentTaskTimeout),
		asynq.MaxRetry(EnrichmentMaxRetry),
	)
	if err != nil {
		return fmt.Errorf("enqueueing saved item enrichment: %w", err)
	}

	return nil
}

// EnqueueSavedItemOrganization queues automatic organization of one saved item.
//
// The saved item's owner is carried alongside its id because the task acts on
// owner-scoped data: the target collection belongs to a user, and the worker
// verifies that the item it was handed is that user's. Scheduling organization
// with an owner that does not match is the one case the consumer treats as a
// failure rather than a no-op.
//
// The caller is the enrichment worker, and it calls this only once the
// enrichment write has committed. That ordering is the whole of this method's
// contract on the caller's side: a task that exists implies a committed row it
// can be resolved against.
//
// A returned error means the task was not queued. The saved item stays exactly
// as enrichment left it, which is a valid saved item that simply remains in the
// collection it was already in.
func (p *Producer) EnqueueSavedItemOrganization(
	ctx context.Context,
	savedItemID uuid.UUID,
	userID uuid.UUID,
) error {
	payload, err := json.Marshal(
		NewOrganizeSavedItemPayload(savedItemID, userID),
	)
	if err != nil {
		return fmt.Errorf("marshalling organization task payload: %w", err)
	}

	task := asynq.NewTask(TaskTypeOrganizeSavedItem, payload)

	_, err = p.client.EnqueueContext(
		ctx,
		task,
		asynq.Queue(QueueOrganization),
		asynq.Timeout(OrganizationTaskTimeout),
		asynq.MaxRetry(OrganizationMaxRetry),
	)
	if err != nil {
		return fmt.Errorf("enqueueing saved item organization: %w", err)
	}

	return nil
}
