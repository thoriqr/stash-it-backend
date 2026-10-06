package enrichment

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/enrichment"
)

// Service enriches one of the user's saved items.
//
// Enrich is the whole application operation. It takes an owner-scoped saved item
// id and nothing else: no user agent, no queue, no HTTP. The synchronous endpoint
// calls it directly, and the background worker will call the same method, so
// enrichment behaviour cannot differ between the two paths.
type Service interface {
	// Enrich enriches exactly one of the user's saved items and returns the item
	// as it stands afterwards.
	//
	// A failure to enrich is not an error. It is recorded as enrichment state on
	// the item and reported through that state, because the item itself is still
	// valid product data. The error return covers the saved item being missing,
	// not the page being unreadable.
	Enrich(
		ctx context.Context,
		userID uuid.UUID,
		savedItemID uuid.UUID,
	) (EnrichResult, error)
}

type service struct {
	repository Repository
	enricher   MetadataEnricher
	log        *zap.Logger
}

// NewService builds the enrichment service.
//
// log is required because the enrichment error is deliberately not returned to
// the caller, so without it a failed page would leave no record anywhere of why.
// Nothing is persisted about the failure, because the schema has no column for a
// reason; the log is the only place that detail exists.
func NewService(
	repository Repository,
	enricher MetadataEnricher,
	log *zap.Logger,
) *service {
	return &service{
		repository: repository,
		enricher:   enricher,
		log:        log,
	}
}

// EnrichResult carries the saved item as it stands after enrichment.
//
// There is no separate success flag. The item's own enrichment_status says
// whether enrichment completed or failed, so reporting it twice would be a second
// source that could disagree with the row.
type EnrichResult struct {
	SavedItem SavedItem
}

// Enrich enriches one of the user's saved items.
//
// The sequence is fixed and deliberate:
//
//  1. Resolve the item inside the caller's ownership scope. Until this succeeds
//     there is no item to enrich and no URL to fetch, so a missing or foreign item
//     is an error and the enricher is never called.
//  2. Fetch and extract. This is the only slow, failure-prone step.
//  3. Record the outcome on the item.
//
// A failed extraction is recorded and returned as a successful operation with a
// failed item, not as an error. That is the difference between "this Saved Item
// does not exist" and "the page behind this Saved Item could not be read", and
// only the first is the caller's mistake.
//
// Nothing here decides a collection or writes collection_id. Enrichment reads
// metadata; organizing is a separate concern.
func (s *service) Enrich(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) (EnrichResult, error) {
	rawURL, err := s.repository.GetSavedItemURLForEnrichment(
		ctx,
		userID,
		savedItemID,
	)
	if err != nil {
		return EnrichResult{}, err
	}

	metadata, enrichErr := s.enricher.Enrich(ctx, rawURL)
	if enrichErr != nil {
		s.log.Warn(
			"enrichment failed",
			zap.String("saved_item_id", savedItemID.String()),
			zap.String("url", rawURL),
			zap.String("kind", string(enrichment.Kind(enrichErr))),
			zap.Error(enrichErr),
		)

		// The status write is the response to a failed enrichment. If it fails
		// too, that is a database problem and is worth returning, because the
		// item would otherwise be left silently claiming to be pending.
		savedItem, failErr := s.repository.FailSavedItemEnrichment(
			ctx,
			userID,
			savedItemID,
		)
		if failErr != nil {
			return EnrichResult{}, failErr
		}

		return EnrichResult{SavedItem: savedItem}, nil
	}

	// What is written is exactly what the extractor returned. A field it did not
	// provide is stored as NULL, and no value is derived, defaulted or inferred
	// from the URL here.
	savedItem, err := s.repository.CompleteSavedItemEnrichment(
		ctx,
		CompleteSavedItemEnrichmentParams{
			UserID:      userID,
			SavedItemID: savedItemID,
			Title:       metadata.Title,
			Platform:    metadata.Platform,
			Description: metadata.Description,
			ImageURL:    metadata.ImageURL,
		},
	)
	if err != nil {
		return EnrichResult{}, err
	}

	return EnrichResult{SavedItem: savedItem}, nil
}
