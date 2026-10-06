package enrichment

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/thoriqr/stash-it-backend/internal/enrichment"
)

// SavedItem is the saved item projection this feature works with.
//
// It carries the enrichment columns, which is the whole reason this feature has
// its own projection rather than borrowing saved_item.SavedItem or
// collection.SavedItem. Neither of those projects description, image_url,
// enrichment_status or last_enriched_at, and enrichment reads and writes all
// four. Following the existing rule that a feature owns the projection its own
// queries produce, this is a separate type with a separate shape.
type SavedItem struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	Url         string
	Domain      pgtype.Text
	Platform    pgtype.Text
	Title       pgtype.Text
	Description pgtype.Text
	ImageURL    pgtype.Text

	// CollectionID is projected so a caller can see where the item lives. It is
	// never written by this feature: enrichment does not organize.
	CollectionID uuid.UUID

	// EnrichmentStatus is one of the values allowed by
	// saved_items_enrichment_status_check. It is the item's enrichment state,
	// not this feature's execution state.
	EnrichmentStatus string

	// LastEnrichedAt is when metadata was last refreshed, so it is set only on a
	// successful enrichment and is NULL until then. It is independent of
	// EnrichmentStatus.
	LastEnrichedAt pgtype.Timestamptz

	CreatedAt pgtype.Timestamptz
	UpdatedAt pgtype.Timestamptz
}

// MetadataEnricher fetches a URL and extracts metadata from the page at it.
//
// It is declared here, in the consuming package, rather than taken as
// *enrichment.Enricher, for the reason every other dependency in this codebase
// is: the consumer states the narrow interface it uses, and that interface is
// what mocks are generated from.
//
// The production implementation is enrichment.Enricher. The interface holds no
// Fiber, sqlc or Asynq, so the future background worker can satisfy this same
// interface without pulling in the HTTP layer.
type MetadataEnricher interface {
	// Enrich returns the metadata found at rawURL, or a classified failure.
	//
	// Returning an error is a normal outcome and not an exceptional one. A page
	// that cannot be fetched, read, or parsed is a fact about the page, and the
	// caller records it rather than treating the request as broken.
	Enrich(
		ctx context.Context,
		rawURL string,
	) (enrichment.Metadata, error)
}

// toPersistenceText converts one extracted value into its stored form.
//
// A nil pointer is SQL NULL. That distinction is the point: an extracted field
// the page did not provide must be stored as NULL rather than as an empty
// string, and a nil is the only honest representation of "the page did not say".
func toPersistenceText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}

	return pgtype.Text{
		String: *value,
		Valid:  true,
	}
}
