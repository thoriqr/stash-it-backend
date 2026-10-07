package organization

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Service runs the automatic organization of one saved item on behalf of a queue
// task.
type Service interface {
	// OrganizeSavedItem files exactly one saved item, or decides there is nothing
	// to do.
	//
	// The error return is about the job, not about the item. A saved item that no
	// longer exists is ErrSavedItemNotFound, a task naming somebody else's item is
	// ErrSavedItemOwnershipMismatch, and both mean the same thing to the queue: no
	// further attempt could produce a different result. Anything else is a
	// database problem worth retrying.
	//
	// Every "nothing to do" case returns no error at all. The item is valid, the
	// queue has nothing left to do, and reporting success is what stops Asynq
	// spending its retry budget rediscovering that the user filed the item
	// themselves.
	OrganizeSavedItem(
		ctx context.Context,
		savedItemID uuid.UUID,
		userID uuid.UUID,
	) error
}

type service struct {
	repository Repository
	log        *zap.Logger
}

// NewService builds the automatic organization service.
//
// log is required because the reason an item was not organized is deliberately
// not stored anywhere: there is no column for it and no organization status is
// introduced, so the log is the only place that detail can exist. It is what
// distinguishes "the user filed this themselves" from "enrichment never found a
// platform" when someone asks why an item is still in Unsorted.
func NewService(
	repository Repository,
	log *zap.Logger,
) *service {
	return &service{
		repository: repository,
		log:        log,
	}
}

// OrganizeSavedItem organizes one saved item in the background.
//
// The whole decision is the repository's, because it has to be made against a
// locked row: the sequence is read the current state, decide, resolve the target,
// move, and splitting those across this layer would leave the read and the write
// able to disagree about what the item was filed in. This service's job is the
// part around them, which is the logging.
//
// Every outcome here is a completion. Whether the item was filed into an existing
// collection, into one this attempt created, or left exactly where it was
// because the user had already filed it, none of them is an enrichment state and
// none of them is written to the item. enrichment_status is untouched throughout:
// an item whose enrichment completed stays completed whether organization moved
// it or skipped it.
func (s *service) OrganizeSavedItem(
	ctx context.Context,
	savedItemID uuid.UUID,
	userID uuid.UUID,
) error {
	outcome, err := s.repository.OrganizeSavedItem(
		ctx,
		savedItemID,
		userID,
	)
	if err != nil {
		// A logged error only. The reason is not stored on the item: the schema has
		// no column for it and adding one is a product decision this feature does
		// not make. The user is never told, because nothing here reaches an API.
		s.log.Warn(
			"automatic organization failed",
			zap.String("saved_item_id", savedItemID.String()),
			zap.String("user_id", userID.String()),
			zap.Error(err),
		)

		return err
	}

	if !outcome.DidMove() {
		// The no-op outcomes are logged at Info rather than Warn. They are the
		// expected shape of this task most of the time: an item with no platform, or
		// one the user filed themselves before the task ran, is the product working
		// as designed rather than a problem to investigate.
		s.log.Info(
			"automatic organization skipped",
			zap.String("saved_item_id", savedItemID.String()),
			zap.String("user_id", userID.String()),
			zap.String("outcome", outcome.String()),
		)

		return nil
	}

	s.log.Info(
		"automatic organization completed",
		zap.String("saved_item_id", savedItemID.String()),
		zap.String("user_id", userID.String()),
		zap.String("outcome", outcome.String()),
	)

	return nil
}
