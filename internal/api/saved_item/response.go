package saved_item

import (
	"time"

	"github.com/google/uuid"
)

// CreatedSavedItemResponse is what a save reports.
//
// It is deliberately the smallest useful representation, and not a shortened copy
// of the detail representation. A save commits before enrichment has run, so at
// this moment domain is the only metadata column carrying a value, and it was
// derived locally from the submitted URL rather than read off the page. Reporting
// platform, title, description, image_url or last_enriched_at here would be a
// handful of nulls presented as though they had been looked up, which reads as
// "this page has nothing" rather than "this page has not been read yet".
//
// enrichment_status is included because the INSERT returns it. It costs no extra
// query, and reporting the stored value beats reporting a constant this code
// assumes: it is what the column's default actually wrote.
//
// This type is separate from SavedItemDetailResponse rather than a subset of it.
// The two endpoints answer different questions, "did my save work" and "what does
// this item look like", and sharing one type would either force the detail response
// to carry fields a save cannot fill, or the save response to omit fields a caller
// may be relying on being absent.
type CreatedSavedItemResponse struct {
	ID           uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	URL          string    `json:"url" example:"https://example.com/articles/1"`
	CollectionID uuid.UUID `json:"collection_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`

	// EnrichmentStatus is the value the row was stored with, which on a fresh save is
	// always pending. Background enrichment has been queued by the time this returns
	// but has not run, so this response never reports completed.
	EnrichmentStatus string `json:"enrichment_status" example:"pending"`
}

// SavedItemDetailResponse is the complete saved item.
//
// It carries the same fields as the saved-item listing in
// GET /collections/:id/saved-items, and for the same reason: without
// enrichment_status a null title is ambiguous between "this page has no title" and
// "this page has not been read yet", and without last_enriched_at there is no way
// to tell whether the metadata above is current.
//
// domain, platform, title, description, image_url and last_enriched_at are nullable
// and are reported as null rather than omitted. A pending or failed enrichment is
// an ordinary state of a saved item and is still returned in full.
type SavedItemDetailResponse struct {
	ID       uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	URL      string    `json:"url" example:"https://example.com/articles/1"`
	Domain   *string   `json:"domain" example:"example.com"`
	Platform *string   `json:"platform" example:"youtube"`
	Title    *string   `json:"title" example:"An interesting article"`

	// Description and ImageURL are null until enrichment supplies them, and stay null
	// afterwards when a page exposed neither.
	Description  *string   `json:"description" example:"A short summary of the page"`
	ImageURL     *string   `json:"image_url" example:"https://example.com/og.png"`
	CollectionID uuid.UUID `json:"collection_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`

	// EnrichmentStatus is pending, completed or failed. Completed means the process
	// ran, not that every field above is populated.
	EnrichmentStatus string `json:"enrichment_status" example:"completed"`

	// LastEnrichedAt is null until an enrichment has succeeded at least once. A failed
	// attempt refreshes nothing, so it does not set this.
	LastEnrichedAt *time.Time `json:"last_enriched_at" example:"2026-10-02T10:31:00Z"`

	CreatedAt time.Time `json:"created_at" example:"2026-10-02T10:30:00Z"`
	UpdatedAt time.Time `json:"updated_at" example:"2026-10-02T10:31:00Z"`
}

// SavedItemCollectionResponse names the collection a saved item is filed in.
//
// It carries only id and name. The caller already arrived knowing the item's id,
// and what it cannot learn from this response is where that item lives and what
// that collection is called. type and system_key belong to the collection listing:
// they describe how a collection came to be, which plays no part in showing one
// item.
//
// Unsorted is not special-cased. It arrives through this type like every other
// collection, which is the same contract it has everywhere else.
type SavedItemCollectionResponse struct {
	ID   uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	Name string    `json:"name" example:"Wishlist"`
}

type CreateSavedItemResponse struct {
	SavedItem CreatedSavedItemResponse `json:"saved_item"`
}

// GetSavedItemResponse is one saved item together with the collection it is filed in.
type GetSavedItemResponse struct {
	SavedItem  SavedItemDetailResponse     `json:"saved_item"`
	Collection SavedItemCollectionResponse `json:"collection"`
}

// DeleteSavedItemResponse reports what deleting the saved item left behind in the
// collection it was in.
//
// It replaces a null data body, because the delete now answers a question the
// caller cannot answer for itself: a saved item's collection is not part of any
// other saved item response, so without this the caller could not even name the
// collection it might want to remove afterwards.
//
// CollectionDeletable is the convenience of the two: it is CollectionEmpty and
// the collection not being the protected Unsorted one. It exists because Unsorted
// is the collection every save lands in, so an empty inbox is the most common way
// for this response to say yes, and a caller that acted on CollectionEmpty alone
// would be turned away on almost every inbox deletion.
//
// Neither field deletes anything. Removing an emptied collection is a separate
// explicit operation that decides what happens to whatever is in it, and this
// response is advisory input to that decision rather than a step of it.
type DeleteSavedItemResponse struct {
	CollectionID uuid.UUID `json:"collection_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	// CollectionEmpty is true when the collection held no other saved item once
	// this one was gone.
	CollectionEmpty bool `json:"collection_empty" example:"true"`
	// CollectionDeletable is true when the collection is now empty and is not the
	// Unsorted collection.
	CollectionDeletable bool `json:"collection_deletable" example:"true"`
}

type DeleteSavedItemAPIResponse struct {
	Data    *DeleteSavedItemResponse `json:"data"`
	Message string                   `json:"message" example:"saved item deleted successfully"`
}

type CreateSavedItemAPIResponse struct {
	Data    *CreateSavedItemResponse `json:"data"`
	Message string                   `json:"message" example:"saved item created successfully"`
}

type GetSavedItemAPIResponse struct {
	Data    *GetSavedItemResponse `json:"data"`
	Message string                `json:"message" example:"saved item retrieved successfully"`
}
