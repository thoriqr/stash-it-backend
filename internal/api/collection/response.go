package collection

import (
	"time"

	"github.com/google/uuid"
)

// CollectionResponse is the collection the saved item ended up in.
//
// system_key is omitted. This endpoint only ever files an item into a user
// collection, where system_key is always NULL by the collections_system_key_check
// constraint. Exposing the field would imply a system collection could be
// targeted here, which it cannot.
type CollectionResponse struct {
	ID        uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	Name      string    `json:"name" example:"Wishlist"`
	Type      string    `json:"type" example:"user"`
	CreatedAt time.Time `json:"created_at" example:"2026-10-02T10:30:00Z"`
	UpdatedAt time.Time `json:"updated_at" example:"2026-10-02T10:30:00Z"`
}

// PutSavedItemSavedItemResponse is the saved item as it now stands, including the
// collection it belongs to.
//
// The enrichment columns are omitted because this operation neither reads nor
// writes them, so there is nothing meaningful to report.
type PutSavedItemSavedItemResponse struct {
	ID           uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	URL          string    `json:"url" example:"https://example.com/articles/1"`
	Domain       *string   `json:"domain" example:"example.com"`
	Platform     *string   `json:"platform" example:"youtube"`
	Title        *string   `json:"title" example:"An interesting article"`
	CollectionID uuid.UUID `json:"collection_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	CreatedAt    time.Time `json:"created_at" example:"2026-10-02T10:30:00Z"`
	UpdatedAt    time.Time `json:"updated_at" example:"2026-10-02T10:30:00Z"`
}

type PutSavedItemIntoCollectionResponse struct {
	Collection CollectionResponse            `json:"collection"`
	SavedItem  PutSavedItemSavedItemResponse `json:"saved_item"`

	// CollectionCreated is true only when this call created the collection. It is
	// false when an existing collection was reused.
	CollectionCreated bool `json:"collection_created" example:"true"`

	// AlreadyInCollection is true when the saved item was already in the target
	// collection, in which case nothing was written. It is mutually exclusive with
	// CollectionCreated.
	AlreadyInCollection bool `json:"already_in_collection" example:"false"`
}

type PutSavedItemIntoCollectionAPIResponse struct {
	Data    *PutSavedItemIntoCollectionResponse `json:"data"`
	Message string                              `json:"message" example:"saved item moved into collection successfully"`
}

// DeleteCollectionAPIResponse is a plain success with no body.
//
// The request already states what should happen to the collection's saved items,
// so the response adds nothing by listing what was done, and returning the removed
// items would report a set the caller already chose to destroy or move. What is
// returned here matches how the saved item delete answered before it began
// reporting collection state: the outcome, not a receipt.
type DeleteCollectionAPIResponse struct {
	Data    *struct{} `json:"data"`
	Message string    `json:"message" example:"collection deleted successfully"`
}
