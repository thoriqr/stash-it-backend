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

// ListedCollectionResponse is a collection as it appears in the user's collection
// list.
//
// It is separate from CollectionResponse rather than an extension of it, because
// that type is also returned by the move endpoint and deliberately omits system_key:
// that endpoint files into a user collection, where system_key is always NULL, and
// including the field there would imply a system collection could be targeted. This
// listing returns every collection a user has, so it needs the field.
//
// system_key is what lets a client recognize Unsorted. The alternative is matching
// on the display name, which is free text and which collections_user_name_unique
// already reserves for exactly this reason. It is also what lets the client know a
// collection may not be deleted before offering to delete it.
type ListedCollectionResponse struct {
	ID        uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	Name      string    `json:"name" example:"Wishlist"`
	Type      string    `json:"type" example:"user"`
	SystemKey *string   `json:"system_key" example:"youtube"`
	CreatedAt time.Time `json:"created_at" example:"2026-10-02T10:30:00Z"`
	UpdatedAt time.Time `json:"updated_at" example:"2026-10-02T10:30:00Z"`
}

// ListCollectionsResponse is one page of the user's collections.
//
// Unsorted appears as the first entry of the first page under every sort, and never
// again on a later page. Collections automatic organization created appear here
// exactly like ones the user named themselves.
type ListCollectionsResponse struct {
	Collections []ListedCollectionResponse `json:"collections"`
}

type ListCollectionsAPIResponse struct {
	Data    *ListCollectionsResponse `json:"data"`
	Message string                   `json:"message" example:"collections retrieved successfully"`
	Meta    ListCollectionsMeta      `json:"meta"`
}

// ListCollectionsMeta is the envelope's meta for GET /collections.
//
// Documentation only, and for the same reason as ListSavedItemsInCollectionMeta: this
// endpoint's own description talks about meta.cursor, and the generated schema for its
// 200 response omitted meta entirely, so the two contradicted each other. It shares
// CursorPageMeta rather than repeating those two fields, because both endpoints report
// the same envelope and two hand-written copies would be one more thing to keep in step.
type ListCollectionsMeta struct {
	Cursor CursorPageMeta `json:"cursor"`
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

// ListedSavedItemResponse is a saved item as the collection listing reports it.
//
// It carries every column the listing projects, including the enrichment ones. Those
// are what make the response meaningful to a caller: without enrichment_status a null
// title is ambiguous between "this page has no title" and "this page has not been read
// yet", and without last_enriched_at there is no way to tell whether metadata is
// current.
//
// description and image_url are nullable because a page is not required to expose
// either, and an item that was never enriched has neither. That is an ordinary state
// rather than a defect, so they are reported as null rather than omitted.
//
// CanonicalURL, SiteName and Author are absent. The extractor can find them, but there
// are no saved_items columns for them and no decision to expose them, so reporting them
// would promise storage that does not exist.
type ListedSavedItemResponse struct {
	ID           uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	URL          string    `json:"url" example:"https://example.com/articles/1"`
	Domain       *string   `json:"domain" example:"example.com"`
	Platform     *string   `json:"platform" example:"youtube"`
	Title        *string   `json:"title" example:"An interesting article"`
	Description  *string   `json:"description" example:"A short summary of the page"`
	ImageURL     *string   `json:"image_url" example:"https://example.com/og.png"`
	CollectionID uuid.UUID `json:"collection_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`

	// EnrichmentStatus is pending, completed or failed. Completed means the process
	// ran, not that every field above is populated, so completed with a null title is
	// normal.
	EnrichmentStatus string `json:"enrichment_status" example:"completed"`

	// LastEnrichedAt is null until an enrichment has succeeded at least once. A failed
	// attempt refreshes nothing, so it does not set this.
	LastEnrichedAt *time.Time `json:"last_enriched_at" example:"2026-10-02T10:31:00Z"`

	CreatedAt time.Time `json:"created_at" example:"2026-10-02T10:30:00Z"`
	UpdatedAt time.Time `json:"updated_at" example:"2026-10-02T10:31:00Z"`
}

// ListedSavedItemsCollectionResponse names the collection a page of saved items was
// read from.
//
// It is the narrowest collection shape this feature has, and deliberately so. The
// caller already arrived knowing the id, and the one thing it cannot know from the
// path is what the collection is called. That is all a listing needs to answer, and
// each of the fields this omits is a question a collection list answers rather than
// this one:
//
//   - type and system_key belong to ListCollectionsResponse. system_key is how a client
//     recognizes Unsorted, and a client reading this response has already named the
//     collection it wants; reporting how that collection was created would invite it to
//     branch on a fact that plays no part in the page it just received.
//   - created_at and updated_at are ordering and bookkeeping details of the
//     collections listing, and have nothing to say about the items inside.
//
// Unsorted is not special-cased. It arrives through this type like every other
// collection, which is the same contract it has everywhere else.
type ListedSavedItemsCollectionResponse struct {
	ID   uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	Name string    `json:"name" example:"YouTube"`
}

// ListSavedItemsInCollectionResponse is one page of a collection's saved items,
// together with the collection they came from.
//
// The collection is reported on every response, including one whose saved_items is
// empty. An empty collection is an ordinary state and still has a name worth showing,
// and reporting the collection unconditionally means a client never has to distinguish
// "no items" from "no collection" to decide what to render.
//
// An empty page is an empty array, never null. A collection holding nothing is an
// ordinary state this product already has, so it is reported as an empty list rather
// than as an absence of one.
type ListSavedItemsInCollectionResponse struct {
	Collection ListedSavedItemsCollectionResponse `json:"collection"`
	SavedItems []ListedSavedItemResponse          `json:"saved_items"`
}

type ListSavedItemsInCollectionAPIResponse struct {
	Data    *ListSavedItemsInCollectionResponse `json:"data"`
	Message string                              `json:"message" example:"saved items retrieved successfully"`
	Meta    ListSavedItemsInCollectionMeta      `json:"meta"`
}

// ListSavedItemsInCollectionMeta is the envelope's meta for this endpoint.
//
// Documentation only. httpx builds the real one; this mirrors it so the generated
// description of a 200 body shows where next_cursor lives, rather than describing a
// two-key envelope while the endpoint actually returns three.
type ListSavedItemsInCollectionMeta struct {
	Cursor CursorPageMeta `json:"cursor"`
}

// CursorPageMeta is the documented shape of meta.cursor.
//
// Documentation only. It mirrors httpx.CursorPage, whose NextCursor field carries no
// omitempty precisely so a final page emits an explicit null rather than a missing key.
// The field is a pointer here for the same reason: the null is the contract, and a
// schema rendering it as an ordinary string would say the opposite.
//
// It is deliberately not a $ref to httpx.CursorPage. That type is the runtime one and
// swag emits no schema for it, so referencing it would produce a definition with no
// properties rather than one describing the fields. Restating two fields keeps the
// generated document readable on its own.
type CursorPageMeta struct {
	// NextCursor is the token for the following page, or null on the final page and on
	// an empty result. It is null whenever has_more is false.
	NextCursor *string `json:"next_cursor" example:"eyJ2IjoxLCJ2YWwiOiIyMDI2LTEwLTAyVDEwOjMwOjAwWiJ9"`

	// HasMore is computed from one row fetched beyond the requested limit, so it is
	// exact rather than inferred from a count. There is deliberately no total: a
	// cursor-paginated response does not know how many results exist overall.
	HasMore bool `json:"has_more" example:"true"`
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
