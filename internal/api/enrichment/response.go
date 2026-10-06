package enrichment

import (
	"time"

	"github.com/google/uuid"
)

// SavedItemResponse is the saved item as enrichment left it.
//
// It carries the enrichment columns because they are the reason this endpoint
// exists: the caller needs to see what was found, and the enrichment state that
// says whether the attempt succeeded.
//
// CanonicalURL, SiteName and Author are not included. The extractor can find
// them, but there are no saved_items columns for them and no decision to expose
// them, so reporting them would promise storage that does not exist.
type SavedItemResponse struct {
	ID           uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	URL          string    `json:"url" example:"https://example.com/articles/1"`
	Domain       *string   `json:"domain" example:"example.com"`
	Platform     *string   `json:"platform" example:"youtube"`
	Title        *string   `json:"title" example:"An interesting article"`
	Description  *string   `json:"description" example:"A short summary of the page"`
	ImageURL     *string   `json:"image_url" example:"https://example.com/og.png"`
	CollectionID uuid.UUID `json:"collection_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`

	// EnrichmentStatus is pending, completed or failed. It is completed when the
	// enrichment process succeeded, which is not the same as every metadata field
	// being present.
	EnrichmentStatus string `json:"enrichment_status" example:"completed"`

	// LastEnrichedAt is null until an enrichment has succeeded at least once. A
	// failed attempt does not set it, because nothing was refreshed.
	LastEnrichedAt *time.Time `json:"last_enriched_at" example:"2026-10-02T10:31:00Z"`

	CreatedAt time.Time `json:"created_at" example:"2026-10-02T10:30:00Z"`
	UpdatedAt time.Time `json:"updated_at" example:"2026-10-02T10:31:00Z"`
}

type EnrichSavedItemResponse struct {
	SavedItem SavedItemResponse `json:"saved_item"`
}

type EnrichSavedItemAPIResponse struct {
	Data    *EnrichSavedItemResponse `json:"data"`
	Message string                   `json:"message" example:"saved item enriched successfully"`
}
