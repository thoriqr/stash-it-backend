package testutil

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// FakeSavedItemEnqueuer stands in for the real queue producer.
//
// The integration tests run against Postgres only, so there is no Redis to enqueue
// into. Injecting a fake at the SavedItemEnqueuer boundary lets a test assert
// that a save scheduled the right work without a queue, which is the part of the
// enqueue path that belongs to this API: which id is queued, and that it is queued
// after the write succeeded.
type FakeSavedItemEnqueuer struct {
	// Err, when set, is returned instead of recording the call, standing in for a
	// queue that cannot be reached. A save must still succeed in that case.
	Err error

	mutex  sync.Mutex
	queued []uuid.UUID
}

func (f *FakeSavedItemEnqueuer) EnqueueSavedItemEnrichment(
	_ context.Context,
	savedItemID uuid.UUID,
) error {
	if f.Err != nil {
		return f.Err
	}

	f.mutex.Lock()
	defer f.mutex.Unlock()

	f.queued = append(f.queued, savedItemID)

	return nil
}

// Queued returns every saved item id the fake was asked to enrich, in order.
func (f *FakeSavedItemEnqueuer) Queued() []uuid.UUID {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	return append([]uuid.UUID(nil), f.queued...)
}
