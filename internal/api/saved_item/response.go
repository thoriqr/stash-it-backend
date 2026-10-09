package saved_item

import (
	"time"

	"github.com/google/uuid"
)

type SavedItemResponse struct {
	ID        uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	URL       string    `json:"url" example:"https://example.com/articles/1"`
	Domain    *string   `json:"domain" example:"example.com"`
	Platform  *string   `json:"platform" example:"youtube"`
	Title     *string   `json:"title" example:"An interesting article"`
	CreatedAt time.Time `json:"created_at" example:"2026-10-02T10:30:00Z"`
	UpdatedAt time.Time `json:"updated_at" example:"2026-10-02T10:30:00Z"`
}

type CreateSavedItemResponse struct {
	SavedItem SavedItemResponse `json:"saved_item"`
}

type GetSavedItemResponse struct {
	SavedItem SavedItemResponse `json:"saved_item"`
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
