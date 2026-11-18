package saved_item

import (
	"context"

	"github.com/google/uuid"
)

// SavedItemEnqueuer schedules background work for a saved item that has just been
// created.
//
// It is declared here, in the consuming package, for the reason every other
// narrow dependency here is: the consumer states the interface it uses, and that
// interface is what mocks are generated from. Nothing about Asynq appears in it,
// so the save flow cannot come to depend on a queue client, a Redis URL or a
// queue's task format. The concrete producer is passed in by the composition root
// and this package never imports the package that defines it.
//
// The method takes only the saved item's id because that is all a task needs: the
// worker loads the item and reads the URL from the row, which is what makes the
// payload unable to disagree with what was stored.
type SavedItemEnqueuer interface {
	// EnqueueSavedItemEnrichment queues background metadata enrichment for one
	// saved item.
	//
	// The error is the caller's to interpret, and the caller treats a failure here
	// as recoverable rather than fatal: the row is already committed by the time
	// this runs, so an unreachable queue leaves a perfectly valid saved item whose
	// enrichment is still pending, exactly as if it had been saved before the
	// worker existed.
	EnqueueSavedItemEnrichment(
		ctx context.Context,
		savedItemID uuid.UUID,
	) error

	// EnqueueSavedItemOrganization queues automatic organization for one saved
	// item.
	//
	// It is used for a client-enriched item: the row is already completed with a
	// platform the server derived from the host, so only the create-or-get
	// collection and the move remain. The organization worker does not fetch the
	// page, which is why this is safe for a host the server must not read.
	EnqueueSavedItemOrganization(
		ctx context.Context,
		savedItemID uuid.UUID,
		userID uuid.UUID,
	) error
}
