package search

import (
	"time"

	"github.com/google/uuid"
)

// SearchSavedItemResponse is one saved item that matched the query.
//
// This is the Search feature's own API shape rather than a reuse of the saved
// item or collection response types. It carries what a search result needs,
// including collection_id so the client can show or link to the collection the
// item currently lives in.
//
// Platform is absent because it is not a searchable field and a search result has
// no use for it. The enrichment columns are absent for the same reason the other
// projections omit them: search neither reads nor reports enrichment state.
//
// Score is absent because relevance is a property of the ordering, not of the
// result. Results arrive already sorted best-first, so a client has everything it
// needs from the position of a row, and exposing the number would commit the API
// to values that depend on the weights and the fuzzy threshold. Those are internal
// to the search SQL and are expected to change. The score is still carried on the
// internal projection, where the ordering needs it.
type SearchSavedItemResponse struct {
	ID  uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	URL string    `json:"url" example:"https://example.org/notes/camera-buying-guide"`
	// Domain and Title are null until background enrichment populates them, which
	// is why they are pointers rather than strings.
	Domain *string `json:"domain" example:"example.org"`
	Title  *string `json:"title" example:"Camera Buying Guide"`
	// CollectionID is the collection the item is currently in. It is returned
	// because the client needs it to show where a result lives, not because
	// collection_id is searchable.
	CollectionID uuid.UUID `json:"collection_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	CreatedAt    time.Time `json:"created_at" example:"2026-10-02T10:30:00Z"`
	UpdatedAt    time.Time `json:"updated_at" example:"2026-10-02T10:30:00Z"`
}

// SearchCollectionResponse is one collection that matched the query.
//
// Type is returned because search includes system collections, and SystemKey is
// returned because it is the stable identity of a system collection. Together they
// let the client tell the Unsorted collection apart from one of the user's own.
//
// Score is absent for the same reason it is absent from SearchSavedItemResponse.
type SearchCollectionResponse struct {
	ID        uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	Name      string    `json:"name" example:"Camera Gear"`
	Type      string    `json:"type" example:"user"`
	SystemKey *string   `json:"system_key" example:"unsorted"`
	CreatedAt time.Time `json:"created_at" example:"2026-10-02T10:30:00Z"`
	UpdatedAt time.Time `json:"updated_at" example:"2026-10-02T10:30:00Z"`
}

// SearchResponse carries both halves of one search.
//
// Both arrays are always present. A search that matched nothing is a successful
// request with empty arrays, never a 404 and never a null. Each array is ordered
// best match first.
type SearchResponse struct {
	Collections []SearchCollectionResponse `json:"collections"`
	SavedItems  []SearchSavedItemResponse  `json:"saved_items"`
}

type SearchAPIResponse struct {
	Data    *SearchResponse `json:"data"`
	Message string          `json:"message" example:"search completed successfully"`
}
