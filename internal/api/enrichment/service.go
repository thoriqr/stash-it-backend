package enrichment

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/clientmetadata"
	"github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/hostset"
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
	//
	// metadata is the client's extraction for a host in CLIENT_ENRICHMENT_HOSTS.
	// It is ignored for every other host, where the server fetches the page itself.
	Enrich(
		ctx context.Context,
		userID uuid.UUID,
		savedItemID uuid.UUID,
		metadata *clientmetadata.Metadata,
	) (EnrichResult, error)
}

type service struct {
	repository Repository
	enricher   MetadataEnricher
	// clientHosts are the hosts the client fetches itself. For these the server
	// never fetches; it stores the client's metadata or records that the client
	// found nothing.
	clientHosts hostset.PlatformMap
	log         *zap.Logger
}

// NewService builds the enrichment service.
//
// log is required because the enrichment error is deliberately not returned to
// the caller, so without it a failed page would leave no record anywhere of why.
// Nothing is persisted about the failure, because the schema has no column for a
// reason; the log is the only place that detail exists.
//
// clientHosts may be empty, which means every host is enriched by the server.
func NewService(
	repository Repository,
	enricher MetadataEnricher,
	clientHosts hostset.PlatformMap,
	log *zap.Logger,
) *service {
	return &service{
		repository:  repository,
		enricher:    enricher,
		clientHosts: clientHosts,
		log:         log,
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
	clientMetadata *clientmetadata.Metadata,
) (EnrichResult, error) {
	rawURL, err := s.repository.GetSavedItemURLForEnrichment(
		ctx,
		userID,
		savedItemID,
	)
	if err != nil {
		return EnrichResult{}, err
	}

	// A host the client fetches itself is never fetched here. The client's
	// extraction is the whole answer: something extracted means completed, and
	// nothing extracted means the attempt failed. Either way the server makes no
	// outbound request, so a host that rejects the server cannot be retried into
	// rejecting it further.
	if s.usesClientEnrichment(rawURL) {
		return s.enrichFromClient(ctx, userID, savedItemID, rawURL, clientMetadata)
	}

	started := time.Now()

	metadata, enrichErr := s.enricher.Enrich(ctx, rawURL)

	elapsed := time.Since(started)

	if enrichErr != nil {
		fields := []zap.Field{
			zap.String("saved_item_id", savedItemID.String()),

			// Query strings and fragments are dropped: they carry tracking
			// parameters and signed tokens, and neither is needed to diagnose a
			// failed fetch.
			zap.String("url", enrichment.SanitizedURL(rawURL)),
			zap.String("kind", string(enrichment.Kind(enrichErr))),
			zap.Int64("elapsed_ms", elapsed.Milliseconds()),
		}

		// Only a status failure has one, so it is omitted rather than logged as a
		// zero that would look like a real status.
		if status := enrichment.Status(enrichErr); status != 0 {
			fields = append(fields, zap.Int("http_status", status))
		}

		// The error is rendered through SafeErrorMessage because a transport
		// failure carries the request URL in its text, including the query string
		// and fragment. The sanitized URL field above is not enough on its own.
		fields = append(
			fields,
			zap.String("error", enrichment.SafeErrorMessage(enrichErr)),
		)

		s.log.Warn("enrichment failed", fields...)

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
	// from the URL here. Price follows the one persistence rule this feature adds:
	// it is stored only when there is also a title. See priceForPersistence.
	priceAmount, priceCurrency := priceForPersistence(metadata)

	savedItem, err := s.repository.CompleteSavedItemEnrichment(
		ctx,
		CompleteSavedItemEnrichmentParams{
			UserID:        userID,
			SavedItemID:   savedItemID,
			Title:         metadata.Title,
			Platform:      metadata.Platform,
			Description:   metadata.Description,
			ImageURL:      metadata.ImageURL,
			PriceAmount:   priceAmount,
			PriceCurrency: priceCurrency,
		},
	)
	if err != nil {
		return EnrichResult{}, err
	}

	// Which fields the page actually exposed, as presence and never as values, so
	// "the page said nothing" is distinguishable from "the preview is complete"
	// without logging anything the user saved.
	s.log.Info(
		"saved item enriched",
		zap.String("saved_item_id", savedItemID.String()),
		zap.Int64("elapsed_ms", elapsed.Milliseconds()),
		zap.Bool("has_title", metadata.Title != nil),
		zap.Bool("has_platform", metadata.Platform != nil),
		zap.Bool("has_description", metadata.Description != nil),
		zap.Bool("has_image", metadata.ImageURL != nil),
	)

	return EnrichResult{SavedItem: savedItem}, nil
}

// enrichFromClient records an enrichment the client performed.
//
// This is the whole of the client-assisted path, and it makes no outbound
// request. The client fetched the page with its own address, parsed it, and sent
// what it found. The server's job is to decide whether that is a completed
// enrichment and to store it, not to ask the page again.
//
// The rule is single and deliberate: at least one non-blank field means the client
// extracted something, so the item is completed; nothing at all means the client
// could not extract, so the item is failed. There is no partial success, because
// completion is the item's enrichment state, not a per-field flag, and the schema
// has no place for one.
//
// The values overwrite what is stored, including clearing fields the client did
// not send. That is the contract: the client sends its full extraction result, and
// a stale value from an earlier attempt must not survive it.
func (s *service) enrichFromClient(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
	rawURL string,
	clientMetadata *clientmetadata.Metadata,
) (EnrichResult, error) {
	metadata, err := clientmetadata.Normalize(shortenInstagramTitle(rawURL, clientMetadata))
	if err != nil {
		return EnrichResult{}, err
	}

	// The client does not own the platform value. It comes from the configured
	// host mapping, so a typo or a localized name on the device cannot create a
	// second collection for the same host.
	var serverPlatform *string
	if platform, ok := s.clientPlatform(rawURL); ok {
		serverPlatform = &platform
	}

	if !metadata.HasValue() {
		savedItem, failErr := s.repository.FailSavedItemEnrichment(
			ctx,
			userID,
			savedItemID,
		)
		if failErr != nil {
			return EnrichResult{}, failErr
		}

		s.log.Info(
			"client enrichment recorded no metadata",
			zap.String("saved_item_id", savedItemID.String()),
		)

		return EnrichResult{SavedItem: savedItem}, nil
	}

	savedItem, err := s.repository.CompleteSavedItemEnrichment(
		ctx,
		CompleteSavedItemEnrichmentParams{
			UserID:      userID,
			SavedItemID: savedItemID,
			Title:       metadata.Title,
			Platform:    serverPlatform,
			Description: metadata.Description,
			ImageURL:    metadata.ImageURL,
		},
	)
	if err != nil {
		return EnrichResult{}, err
	}

	// Presence, never values: the log must not record what the user saved.
	s.log.Info(
		"client enrichment stored metadata",
		zap.String("saved_item_id", savedItemID.String()),
		zap.Bool("has_title", metadata.Title != nil),
		zap.Bool("has_platform", serverPlatform != nil),
		zap.Bool("has_description", metadata.Description != nil),
		zap.Bool("has_image", metadata.ImageURL != nil),
	)

	return EnrichResult{SavedItem: savedItem}, nil
}

// shortenInstagramTitle applies the extractor's Instagram title rule to the
// client's metadata before it is validated.
//
// Order matters: Instagram's Open Graph title is the author plus the whole
// caption, and a caption can exceed the title bound, so validating first would
// reject a request whose title the rule turns into a short label.
func shortenInstagramTitle(
	rawURL string,
	metadata *clientmetadata.Metadata,
) *clientmetadata.Metadata {
	if metadata == nil || metadata.Title == nil {
		return metadata
	}

	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return metadata
	}

	metadata.Title = enrichment.ShortenInstagramTitle(parsed, metadata.Title)

	return metadata
}

// usesClientEnrichment reports whether the saved item's host is one the client
// fetches itself.
//
// The URL comes from the stored row and has already been validated at save time,
// so a parse failure here simply means "not in the map".
func (s *service) usesClientEnrichment(rawURL string) bool {
	if len(s.clientHosts) == 0 {
		return false
	}

	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}

	return s.clientHosts.Contains(parsed.Hostname())
}

// clientPlatform reports the platform configured for the saved item's host.
func (s *service) clientPlatform(rawURL string) (string, bool) {
	if len(s.clientHosts) == 0 {
		return "", false
	}

	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", false
	}

	return s.clientHosts.PlatformFor(parsed.Hostname())
}

// priceForPersistence applies the Title-dependent persistence rule for price.
//
// Price is persisted only when the extraction also produced a title. Title is not
// a requirement for successful enrichment and it does not gate any other field; it
// gates price alone, because a price without the product title that gives it
// meaning is not trustworthy as standalone product information.
//
// Every other combination yields two NULLs, which is what clears a stale price from
// an earlier successful refresh. A valid amount is kept even when its currency could
// not be normalized, in which case the currency column is NULL.
func priceForPersistence(
	metadata enrichment.Metadata,
) (
	amount *string,
	currency *string,
) {
	if metadata.Title == nil || metadata.Price == nil {
		return nil, nil
	}

	priceAmount := metadata.Price.Amount

	var priceCurrency *string
	if metadata.Price.Currency != "" {
		mapped := metadata.Price.Currency
		priceCurrency = &mapped
	}

	return &priceAmount, priceCurrency
}
