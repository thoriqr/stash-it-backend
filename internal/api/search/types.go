package search

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// SearchSavedItem is the saved item projection this feature returns from a
// search.
//
// It is deliberately Search's own projection rather than a reuse of
// saved_item.SavedItem or the collection feature's SavedItem. Those two carry
// the columns their own operations happen to read; this one carries what a
// search result actually needs, which includes collection_id because the client
// shows where an item currently lives. Sharing a type across features would tie
// search to changes in features it has nothing to do with.
//
// Platform is absent because it is not a searchable field and a search result
// has no use for it. The enrichment columns are absent for the same reason
// saved_item.SavedItem omits them: search neither reads nor reports enrichment
// state, so there is nowhere for it to appear.
type SearchSavedItem struct {
	ID           uuid.UUID
	URL          string
	Domain       pgtype.Text
	Title        pgtype.Text
	CollectionID uuid.UUID
	CreatedAt    pgtype.Timestamptz
	UpdatedAt    pgtype.Timestamptz
	// Score is the relevance score the row was ordered by. It is carried so the
	// ordering can be explained rather than only observed.
	Score float64
}

// SearchCollection is the collection projection this feature returns from a
// search.
//
// Type and SystemKey are carried because system collections take part in search:
// a search for "unsorted" should find the Unsorted collection, and the client
// cannot tell a system collection from a user collection without them. SystemKey
// is the stable identity of a system collection and is never its display name.
//
// Score is the relevance score the row was ordered by, as for SearchSavedItem.
type SearchCollection struct {
	ID        uuid.UUID
	Name      string
	Type      string
	SystemKey pgtype.Text
	CreatedAt pgtype.Timestamptz
	UpdatedAt pgtype.Timestamptz
	Score     float64
}
