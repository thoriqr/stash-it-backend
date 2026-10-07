package enrichment

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/enrichment"
)

// Service runs the enrichment of one saved item on behalf of a queue task.
type Service interface {
	// EnrichSavedItem enriches exactly one saved item and records the outcome.
	//
	// The error return is about the job, not about the page. A page that could not
	// be enriched is recorded on the item and its error is returned as it was,
	// classified, so the caller can decide whether trying again could help. A saved
	// item that no longer exists is reported as ErrSavedItemNotFound. Anything else
	// is a database problem.
	EnrichSavedItem(ctx context.Context, savedItemID uuid.UUID) error
}

type service struct {
	repository Repository
	enricher   MetadataEnricher
	organizer  SavedItemOrganizer
	log        *zap.Logger
}

// NewService builds the background enrichment service.
//
// organizer schedules the automatic organization that follows a successful
// enrichment. It may be nil, which is how a process that enriches without
// arranging for anything else is expressed, and an enrichment still completes.
//
// log is required for the same reason it is required by the API feature: the
// reason a page could not be enriched is deliberately not stored anywhere, since
// the schema has no column for one, so the log is the only place that detail can
// exist.
func NewService(
	repository Repository,
	enricher MetadataEnricher,
	organizer SavedItemOrganizer,
	log *zap.Logger,
) *service {
	return &service{
		repository: repository,
		enricher:   enricher,
		organizer:  organizer,
		log:        log,
	}
}

// EnrichSavedItem enriches one saved item in the background.
//
// The sequence matches the synchronous flow, statement for statement, so the two
// cannot produce different rows for the same page:
//
//  1. Load the item. Until this succeeds there is no URL to fetch, and a deleted
//     item is reported as ErrSavedItemNotFound so the handler can discard the task
//     instead of retrying it.
//  2. Fetch and extract. This is the only slow, failure-prone step.
//  3. Record the outcome on the item.
//
// A failed extraction is recorded and its error returned unchanged. The
// classification travels with that error because internal/enrichment already
// attaches one, and returning it as it is leaves the handler free to read
// enrichment.Kind without this package having to restate what it means. The
// synchronous flow cannot do the same thing, because its caller is an HTTP
// response that reports enrichment state as data rather than as a failure; both
// behaviours are the same recorded status.
//
// An empty metadata result is not a failure. It is recorded as completed, because
// the process succeeded and the page simply said nothing about itself.
//
// Nothing here decides a collection or writes collection_id. Enrichment reads
// metadata; organizing is a separate concern.
func (s *service) EnrichSavedItem(
	ctx context.Context,
	savedItemID uuid.UUID,
) error {
	savedItem, err := s.repository.GetSavedItemForBackgroundEnrichment(
		ctx,
		savedItemID,
	)
	if err != nil {
		return err
	}

	metadata, enrichErr := s.enricher.Enrich(ctx, savedItem.Url)
	if enrichErr != nil {
		s.log.Warn(
			"background enrichment failed",
			zap.String("saved_item_id", savedItemID.String()),
			zap.String("user_id", savedItem.UserID.String()),
			zap.String("url", savedItem.Url),
			zap.String("kind", string(enrichment.Kind(enrichErr))),
			zap.Error(enrichErr),
		)

		// Recording the failure is the response to a failed enrichment, and it is
		// recorded the same way whether the queue will try again or not. The row
		// is the durable record of the attempt that just happened; how many more
		// attempts Asynq makes is queue execution mechanics and is deliberately
		// not part of the saved item's state, so there is no retrying or
		// processing status and none is written.
		//
		// A retry that later succeeds overwrites this with completed, which is
		// the same thing the synchronous endpoint produces for the same page.
		//
		// If the status write fails too, that is a database problem rather than a
		// fact about the page, so it is returned as itself and the task is
		// retried: the item would otherwise be left silently claiming to be
		// pending.
		if err := s.repository.FailSavedItemEnrichment(
			ctx,
			savedItemID,
		); err != nil {
			return err
		}

		return enrichErr
	}

	// What is written is exactly what the extractor returned. A field it did not
	// provide is stored as NULL, and no value is derived, defaulted or inferred
	// from the URL here.
	completed, err := s.repository.CompleteSavedItemEnrichment(
		ctx,
		CompleteSavedItemEnrichmentParams{
			SavedItemID: savedItemID,
			Title:       metadata.Title,
			Platform:    metadata.Platform,
			Description: metadata.Description,
			ImageURL:    metadata.ImageURL,
		},
	)
	if err != nil {
		return err
	}

	// Organization is scheduled from here, after the write has returned.
	//
	// The write is a single statement in autocommit, so a row returned by it is
	// committed by the time this function holds it. That is the whole reason the
	// handoff is here rather than before the write: a task created earlier could
	// name a row the database had not accepted yet, and the organization worker
	// would go looking for an item that did not exist.
	//
	// The item's own owner comes from the write's own result rather than from the
	// row loaded at the start, because that result is the committed truth and the
	// earlier load has since been overtaken by it.
	//
	// Only a successful enrichment with a platform schedules anything. A failed
	// enrichment has already returned above, and a page that exposed no platform
	// has nothing to organize on: there is no "pending organization" state to
	// record and no sweep to find them later, so those items simply stay where
	// they are until a user asks for them to be enriched again.
	s.scheduleOrganization(ctx, completed)

	return nil
}

// scheduleOrganization queues automatic organization for a completed item that
// enrichment produced a platform for.
//
// A failure here is logged and swallowed. The enrichment write has already
// committed and the saved item is valid whatever happens next, so reporting an
// error here would make Asynq retry the enrichment — which would re-fetch the
// page and overwrite metadata that is already correct — in order to schedule
// work that is secondary to it. The item stays in Unsorted, which is exactly where
// it was before enrichment ran.
//
// The ids are logged rather than the item's URL: the URL is user-supplied content
// and nothing about diagnosing a scheduling failure needs it.
func (s *service) scheduleOrganization(
	ctx context.Context,
	completed CompletedSavedItem,
) {
	if s.organizer == nil || !completed.HasPlatform() {
		return
	}

	if err := s.organizer.EnqueueSavedItemOrganization(
		ctx,
		completed.SavedItemID,
		completed.UserID,
	); err != nil {
		s.log.Warn(
			"saved item enriched but automatic organization was not queued",
			zap.String("saved_item_id", completed.SavedItemID.String()),
			zap.String("user_id", completed.UserID.String()),
			zap.Error(err),
		)
	}
}
