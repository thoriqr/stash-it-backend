package saved_item

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// SavedItem is the saved item projection this feature works with, carrying the
// columns the saved items queries select.
//
// It replaces the generated saveditemdb.SavedItem table model, which cannot
// serve this feature: migration 000022 gave saved_items more columns than the
// queries project, so sqlc emits a distinct row type per query rather than
// reusing the model. Owning the projection here keeps the repository interface,
// the service and the mapper on one type without depending on a generated struct
// that sqlc does not consider used.
//
// The field shape matches the generated model exactly, including Url rather than
// URL, so switching between the two is a pure symbol swap with no field churn.
//
// This is the complete saved item: every column a client needs to render one,
// including collection_id and the enrichment columns. Create and Get are the only
// readers left in this feature, and Get has to report all of it, so a narrower
// projection would only mean a second near-identical type plus a decision about
// which of the two a new caller should be handed.
//
// The collection feature owns its own separate projection, which is the same set
// of columns shaped for paging over a collection rather than for describing one
// item. It stays separate for that reason.
type SavedItem struct {
	ID       uuid.UUID
	UserID   uuid.UUID
	Url      string
	Domain   pgtype.Text
	Platform pgtype.Text
	Title    pgtype.Text

	// Description and ImageURL are NULL until enrichment supplies them, and stay
	// NULL afterwards when a page exposed neither. Neither being present is an
	// ordinary outcome and not a defect.
	Description pgtype.Text
	ImageURL    pgtype.Text

	// CollectionID is the collection this item is filed in. Every saved item belongs
	// to exactly one, and the column is NOT NULL.
	CollectionID uuid.UUID

	// EnrichmentStatus is pending, completed or failed. It describes whether the
	// enrichment process ran, not whether every field above is populated, so
	// completed with a null title is a normal state. It is NOT NULL with a default,
	// so it is a plain string and never an absent one.
	EnrichmentStatus string

	// LastEnrichedAt is NULL until an enrichment has succeeded at least once. A failed
	// attempt refreshes nothing, so it does not set this.
	LastEnrichedAt pgtype.Timestamptz

	CreatedAt pgtype.Timestamptz
	UpdatedAt pgtype.Timestamptz
}

// SavedItemCollection is the collection a saved item is filed in, named.
//
// It is deliberately the narrowest useful shape. A caller opening a saved item needs
// to know which collection it is in and what that collection is called, and nothing
// else: the questions type and system_key answer belong to the collection listing,
// not to one item's detail page.
//
// Unsorted is not special-cased. It arrives through this type like every other
// collection.
type SavedItemCollection struct {
	ID uuid.UUID

	// Name is the collection's display name. collections.name is NOT NULL, so this is
	// always a real value rather than an optional one.
	Name string
}

// DeletedSavedItem is what the delete statement reported back about the row it
// removed.
//
// It is a dedicated type rather than a new field on SavedItem because SavedItem is
// the projection every read in this feature shares, and this one has a narrower job:
// the delete only needs the row's own collection_id, which its DELETE statement
// reports. It does not need the item's metadata or its collection's name, and reading
// those for this endpoint would put columns in front of it that it does not return.
type DeletedSavedItem struct {
	ID uuid.UUID

	// CollectionID is the collection the deleted row was actually in. It is
	// reported by the DELETE itself rather than read beforehand, so it is the
	// deleted row's own value and cannot disagree with the row that is gone.
	CollectionID uuid.UUID
}

// ListedSavedItem is a saved item as the recent saved items listing reports it.
//
// It is a separate projection from SavedItem because it carries one column SavedItem
// cannot: CollectionName. A listing spans every collection the user owns, so unlike
// a per-collection listing the caller cannot learn from the request which collection
// any given row is in, and a bare collection_id would leave a client unable to name
// where an item lives without a request per row.
//
// CollectionName is read from the same statement as the item, by joining collections,
// so reporting it costs nothing beyond the read that was happening anyway.
type ListedSavedItem struct {
	ID       uuid.UUID
	UserID   uuid.UUID
	Url      string
	Domain   pgtype.Text
	Platform pgtype.Text
	Title    pgtype.Text

	// Description and ImageURL are NULL until enrichment supplies them, and stay
	// NULL afterwards when a page exposed neither. Neither being present is an
	// ordinary outcome and not a defect, and neither is a reason to leave the item
	// out of the history.
	Description pgtype.Text
	ImageURL    pgtype.Text

	// CollectionID is the collection this item is filed in. Every saved item belongs
	// to exactly one, and the column is NOT NULL, so it always has a value. It is
	// reported alongside the collection name because the id alone is what makes the
	// item addressable and the name is what makes it legible.
	CollectionID uuid.UUID

	// EnrichmentStatus is pending, completed or failed. The listing reports it and
	// never filters on it: all three are ordinary states of a saved item, and a
	// history that hid the two it had not finished processing would be a history of
	// only the items that went well.
	EnrichmentStatus string

	// LastEnrichedAt is NULL until an enrichment has succeeded at least once. A
	// failed attempt refreshes nothing, so it does not set this.
	LastEnrichedAt pgtype.Timestamptz

	CreatedAt pgtype.Timestamptz
	UpdatedAt pgtype.Timestamptz

	// CollectionName is the display name of CollectionID. collections.name is NOT
	// NULL, so it is always a real value rather than an optional one.
	CollectionName string
}

// ListSavedItemsParams is one validated request to list a user's recent saved items.
type ListSavedItemsParams struct {
	UserID uuid.UUID

	// Limit is the number of saved items to return, already defaulted and clamped by
	// the service. The repository asks for one more so HasMore is exact.
	Limit int

	// Cursor resumes a listing, or is nil for the first page. It is the decoded token,
	// never the raw query string.
	Cursor *ListSavedItemsCursor
}

// ListSavedItemsCursor is the position a page ended at, encoded into an opaque
// token.
//
// It is a feature payload rather than part of internal/pagination, and it is
// deliberately the smallest cursor payload in the project. It has no Sort and no
// Group: this listing has one ordering and no pinned row, so a token cannot be
// mismatched against anything and there is nothing for a sort comparison to reject.
// Carrying fields that can never vary here would mean every validation below had a
// branch that could not be reached.
//
// Value and ID are pointers so an absent field stays distinguishable from a zero one.
// A truncated token has to be rejected rather than read as the top of the history,
// which would silently return the newest page to a client that asked to resume.
type ListSavedItemsCursor struct {
	// Version is the payload format, checked against pagination.CurrentVersion. A
	// cursor can outlive a deployment, so a token written by another build has to
	// fail cleanly rather than be misread as a position.
	Version int `json:"v"`

	// Value is the item's created_at at that position, as RFC3339Nano because the
	// payload travels as JSON.
	//
	// The service checks it is a real timestamp and records the parsed form in
	// Timestamp before any query runs, so Value never reaches a query unvalidated.
	Value *string `json:"val"`

	// ID is the tie-breaker at that position, which is what makes created_at a total
	// order.
	ID *uuid.UUID `json:"i"`

	// Timestamp is Value already parsed into the type sqlc expects for a timestamptz
	// column. It is derived, not transmitted: json:"-" keeps it out of the token, so
	// the payload stays the wire contract and this is never a second copy of the
	// position that could disagree with it.
	//
	// Because the service fills it, the repository never parses a client-supplied
	// value and can only pass along something that was already accepted.
	Timestamp pgtype.Timestamptz `json:"-"`
}

// CursorVersion reports the payload format so pagination.DecodeVersioned can check
// it without this package needing to know the field.
func (c ListSavedItemsCursor) CursorVersion() int {
	return c.Version
}

// hasPosition reports whether the cursor carries a position to resume from.
func (c *ListSavedItemsCursor) hasPosition() bool {
	return c.Value != nil && c.ID != nil
}

// ListSavedItemsResult is one page of recent saved items plus the cursor state that
// follows it.
type ListSavedItemsResult struct {
	// SavedItems is the page itself. It is made with a length by the repository, so
	// an empty history serializes as [] rather than null.
	SavedItems []ListedSavedItem
	Limit      int

	// HasMore is exact: the repository fetched one row beyond Limit and this says
	// whether that row existed, rather than counting every item to find out.
	HasMore bool

	// NextCursor is the token for the following page, or nil when HasMore is false.
	// It is derived from the last item actually returned, never from the lookahead
	// row, so a page boundary never skips an item the client did not receive.
	NextCursor *string
}
