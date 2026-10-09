package search

import (
	"time"

	"github.com/google/uuid"
)

// SearchSavedItemResponse is one saved item that matched the query.
//
// This is the Search feature's own API shape rather than a reuse of the saved
// item or collection response types. It carries what a search result needs to be
// rendered, which is a different question from what a saved item detail response
// carries.
//
// Title, Domain and ImageURL are pointers because the columns are nullable, and
// null is reported rather than a value substituted for one of them: a title is
// never assembled from the domain or the URL, so a result whose page has not
// been read reports nulls. A client that needs a label for such a result chooses
// its own fallback rather than being handed one that was never on the page.
//
// EnrichmentStatus is pending, completed or failed, and is what makes a null
// title unambiguous: without it, a result could not say whether the page exposed
// no title or simply has not been read yet. Completed means the process ran, not
// that every field above is populated, so completed with a null title is normal.
//
// Platform, description, last_enriched_at and updated_at are absent. None is
// needed to show a result, and each belongs to the response that already carries
// it.
//
// Score is absent because relevance is a property of the ordering, not of the
// result. Results arrive already sorted best-first, so a client has everything it
// needs from the position of a row, and exposing the number would commit the API
// to values that depend on the weights and the fuzzy threshold. Those are internal
// to the search SQL and are expected to change. The score is still carried on the
// internal projection, where the ordering needs it.
type SearchSavedItemResponse struct {
	ID               uuid.UUID                     `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	Title            *string                       `json:"title" example:"Camera Buying Guide"`
	URL              string                        `json:"url" example:"https://example.org/notes/camera-buying-guide"`
	Domain           *string                       `json:"domain" example:"example.org"`
	ImageURL         *string                       `json:"image_url" example:"https://example.org/og.png"`
	EnrichmentStatus string                        `json:"enrichment_status" example:"completed"`
	Collection       SearchSavedItemCollectionInfo `json:"collection"`
	CreatedAt        time.Time                     `json:"created_at" example:"2026-10-02T10:30:00Z"`
}

// SearchSavedItemCollectionInfo names the collection a result is filed in.
//
// It is the narrowest collection shape this feature has, and it is nested rather
// than a second field on the result because the collection is only ever read
// alongside the item that lives in it. Reporting it here is what saves the client
// a request: a search result can say where it is and what that collection is
// called without anything else being asked for.
//
// Unsorted is not special-cased. An item in the inbox arrives through this type
// like every other result, which is the same contract the collection listing
// gives it.
type SearchSavedItemCollectionInfo struct {
	ID   uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	Name string    `json:"name" example:"Camera Gear"`
}

// SearchCollectionResponse is one collection that matched the query.
//
// A collection result is a destination, not a record of one, so it reports an id
// to navigate with and a name to render. Type and SystemKey are absent: telling a
// system collection from a user one is a question GET /collections already
// answers, and nothing in a search result depends on the answer.
//
// Score is absent for the same reason it is absent from SearchSavedItemResponse,
// as are created_at and updated_at.
type SearchCollectionResponse struct {
	ID   uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	Name string    `json:"name" example:"Camera Gear"`
}

// SearchResponse carries both halves of one search.
//
// Both arrays are always present. A search that matched nothing is a successful
// request with empty arrays, never a 404 and never a null. Each array is ordered
// best match first.
type SearchResponse struct {
	SavedItems  []SearchSavedItemResponse  `json:"saved_items"`
	Collections []SearchCollectionResponse `json:"collections"`
}

type SearchAPIResponse struct {
	Data    *SearchResponse `json:"data"`
	Message string          `json:"message" example:"search completed successfully"`
}
