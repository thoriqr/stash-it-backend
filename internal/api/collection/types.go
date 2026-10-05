package collection

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

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
