package collection

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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
