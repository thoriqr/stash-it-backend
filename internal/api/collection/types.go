package collection

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	collectiondb "github.com/thoriqr/stash-it-backend/internal/api/collection/generated"
)

// SavedItemsAction is what a collection deletion should do with the saved items
// currently filed in the collection being removed.
//
// It is an explicit choice rather than a default, and there is deliberately no
// third option. A collection holding saved items cannot simply disappear, because
// saved_items.collection_id is NOT NULL and the foreign key protects those items;
// so a deletion must state where they go. Staying silent would leave the backend to
// choose between destroying a user's saved items and losing them some other way.
type SavedItemsAction string

const (
	// SavedItemsActionDelete deletes the collection's saved items along with the
	// collection. The caller's choice: the items are being discarded deliberately.
	SavedItemsActionDelete SavedItemsAction = "delete"

	// SavedItemsActionMove files the collection's saved items into another
	// collection, named by id, and then deletes the collection.
	//
	// Unsorted is a valid target and is referred to by its id like any other
	// collection. There is no separate mode for it, because there is nothing about
	// it that needs different handling.
	SavedItemsActionMove SavedItemsAction = "move"
)

// DeleteCollectionParams is one validated request to delete a collection.
type DeleteCollectionParams struct {
	UserID       uuid.UUID
	CollectionID uuid.UUID

	// Action is what happens to the saved items in the collection. It is always
	// one of the two values above: the service rejects anything else rather than
	// choosing on the caller's behalf.
	Action SavedItemsAction

	// TargetCollectionID is where the saved items go when Action is
	// SavedItemsActionMove, and is unused otherwise. The service requires it for
	// 'move' and refuses it for 'delete', so this field always carries a decision
	// rather than a default.
	TargetCollectionID uuid.UUID
}

// CollectionSort is the order collections are listed in.
//
// Every ordering has the same shape: the Unsorted collection first, then the rest by
// the chosen key. Unsorted is pinned by position rather than filtered out, so it is
// present under every sort rather than being a special case in each one.
type CollectionSort string

const (
	// CollectionSortNewest is most recently created first. This is the default.
	CollectionSortNewest CollectionSort = "newest"

	// CollectionSortOldest is earliest created first.
	CollectionSortOldest CollectionSort = "oldest"

	// CollectionSortName is display name ascending, compared the way collection
	// names are stored and compared everywhere else: lower(btrim(name)). That keeps
	// the listing order and the uniqueness constraint from holding different
	// opinions about what two names being "the same" means.
	CollectionSortName CollectionSort = "name"
)

// IsValid reports whether the sort is one this feature supports.
//
// The service validates through this rather than through a validate tag on the
// request, because the same check has to run again for a cursor's own sort value: a
// token is a string this build did not write, so it cannot be trusted to hold a
// supported value. One place that decides is one place that cannot disagree.
func (s CollectionSort) IsValid() bool {
	switch s {
	case CollectionSortNewest, CollectionSortOldest, CollectionSortName:
		return true
	default:
		return false
	}
}

// ListCollectionsParams is one validated request to list collections.
type ListCollectionsParams struct {
	UserID uuid.UUID

	// Sort is always one of the supported values. The service resolves the default
	// before this is built, so no caller passes an empty sort.
	Sort CollectionSort

	// Limit is the number of collections to return, already defaulted and clamped by
	// the service. The repository asks for one more than this so HasMore is exact.
	Limit int

	// Cursor resumes a listing, or is nil for the first page. It is the decoded
	// token, never the raw query string: decoding and validating it is the service's
	// job, not the repository's.
	Cursor *ListCollectionsCursor
}

// The two cursor groups. Which one a cursor belongs to decides the query it selects,
// because only one of them carries a position to resume from.
const (
	// cursorGroupAfterUnsorted means the page ended on Unsorted, so the next page
	// starts at the top of the regular ordering.
	cursorGroupAfterUnsorted = 0

	// cursorGroupAfterRow means the cursor carries a position within the regular
	// ordering.
	cursorGroupAfterRow = 1
)

// ListCollectionsCursor is the position a page ended at, encoded into an opaque
// token.
//
// It is a feature payload rather than part of internal/pagination, because what a
// page needs to record is specific to this listing. Only the envelope is shared.
//
// Every field that could be absent is a pointer. That is the whole reason: with a
// plain int Group, a truncated payload decodes to 0, which is exactly the
// "after Unsorted" group, so a malformed token would silently read as a valid one
// and skip the collection this product requires to appear first. Pointers keep
// "absent" distinct from "zero".
type ListCollectionsCursor struct {
	// Version is the payload format, checked against pagination.CurrentVersion. A
	// cursor outlives the request that produced it and can outlive a deployment, so a
	// token written by another build must fail cleanly rather than be misread as a
	// valid position.
	Version int `json:"v"`

	// Sort is the ordering this cursor was issued for. Reusing it with a different
	// sort is rejected, because the stored position means nothing under an ordering
	// that does not share it.
	Sort CollectionSort `json:"s"`

	// Group is cursorGroupAfterUnsorted when the page ended on Unsorted, and
	// cursorGroupAfterRow when it ended on a regular collection.
	Group *int `json:"g"`

	// Value is the position within the ordering: created_at for the time-based
	// sorts, and lower(btrim(name)) for the name sort. Stored as a string because
	// the payload travels as JSON and the two sorts need different types. It is nil
	// for the after-Unsorted group, which has no position.
	//
	// The service checks that this is a timestamp the time-based sorts can use
	// before any query runs, and records the parsed form in Timestamp. Value is
	// therefore never handed to a query unvalidated.
	Value *string `json:"val"`

	// ID is the tie-breaker at that position, and nil for the after-Unsorted group.
	ID *uuid.UUID `json:"i"`

	// Timestamp is Value already parsed into the type sqlc expects for a timestamptz
	// column. It is derived, not transmitted: json:"-" keeps it out of the token, so
	// the payload stays the wire contract and this is never a second copy of the
	// position that could disagree with it.
	//
	// It is set for the time-based sorts and left unset for the name sort, whose value
	// is already the string the query compares. Because the service fills it, the
	// repository never parses a client-supplied value and can only pass along
	// something that was already accepted.
	Timestamp pgtype.Timestamptz `json:"-"`
}

// CursorVersion reports the payload format so pagination.DecodeVersioned can check
// it without this package needing to know the field.
func (c ListCollectionsCursor) CursorVersion() int {
	return c.Version
}

// hasPosition reports whether the cursor carries a position to resume from, which
// only the after-row group does.
//
// The after-Unsorted group deliberately carries none: it means the previous page
// ended on the pinned row, so the next page starts at the top of the regular
// ordering and there is nothing to compare against.
func (c *ListCollectionsCursor) hasPosition() bool {
	return c.Group != nil && *c.Group == cursorGroupAfterRow
}

// ListCollectionsResult is one page of collections plus the cursor state that
// follows it.
type ListCollectionsResult struct {
	Collections []collectiondb.Collection
	Limit       int
	Sort        CollectionSort

	// HasMore is exact: the repository fetched one row beyond Limit and this says
	// whether that row existed. It is never inferred from a count, because counting
	// every collection on every request is the work keyset pagination avoids.
	HasMore bool

	// NextCursor is the token for the following page, or nil when HasMore is false.
	// It is derived from the last row actually returned, never from the lookahead
	// row: a cursor built from a row the client never received would resume past it
	// and skip one collection at every page boundary.
	NextCursor *string
}

type CollectionType string

const (
	// CollectionTypeSystem is a collection the product owns, identified by its
	// CollectionSystemKey rather than by its display name.
	CollectionTypeSystem CollectionType = "system"

	// CollectionTypeUser is a collection the user owns. User collections have no
	// system key and only ever come into existence as part of putting a saved
	// item into them, so an empty user collection is never created on purpose.
	CollectionTypeUser CollectionType = "user"
)

type CollectionSystemKey string

const (
	// CollectionSystemKeyUnsorted is the permanent system collection every user
	// receives when their registration is finalized. It is where new saved items
	// land and it is allowed to become empty.
	CollectionSystemKeyUnsorted CollectionSystemKey = "unsorted"
)

// SavedItem is the saved item projection this feature works with. It carries the
// columns this operation reads and returns, plus collection_id, the only column
// the operation changes.
//
// The enrichment columns are deliberately absent. Putting a saved item into a
// collection neither inspects nor reports enrichment state, so projecting
// enrichment_status, last_enriched_at, description or image_url here would only
// invite code to depend on them.
type SavedItem struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	URL          string
	Domain       pgtype.Text
	Platform     pgtype.Text
	Title        pgtype.Text
	CollectionID uuid.UUID
	CreatedAt    pgtype.Timestamptz
	UpdatedAt    pgtype.Timestamptz
}
