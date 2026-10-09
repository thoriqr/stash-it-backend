package search

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// SearchSavedItem is the saved item projection this feature returns from a
// search.
//
// It is deliberately Search's own projection rather than a reuse of
// saved_item.SavedItem or the collection feature's ListedSavedItem. Those two
// carry the columns their own operations happen to read; this one carries what a
// search result actually needs. Sharing a type across features would tie search
// to changes in features it has nothing to do with.
//
// Collection is the collection the result lives in. It is read in full rather than
// as an id alone, because a result that reported the id would leave the client unable
// to show where an item is without a second request, and that is what the result is
// for. Neither field is searchable: they are reported, never matched.
//
// Platform, description and last_enriched_at are absent. Platform is not a
// searchable field and a result has no use for it, and the other two are
// enrichment state this response does not report. updated_at is absent for the
// same reason: nothing orders by it and nothing reports it.
//
// Title, Domain and ImageURL keep the columns' nullable semantics exactly. None
// of them is ever substituted for another, so an item whose page has not been
// read reports three nulls rather than a title assembled from its URL.
type SearchSavedItem struct {
	ID         uuid.UUID
	Title      pgtype.Text
	URL        string
	Domain     pgtype.Text
	ImageURL   pgtype.Text
	Collection SearchCollectionRef

	// EnrichmentStatus is pending, completed or failed. It is reported because a
	// null title is otherwise ambiguous between "this page has no title" and "this
	// page has not been read yet".
	EnrichmentStatus string

	CreatedAt pgtype.Timestamptz

	// Score is the relevance score the row was ordered by. It is carried so the
	// ordering can be explained rather than only observed, and is not reported.
	Score float64
}

// SearchCollectionRef is the collection a saved item result lives in.
//
// It is the narrowest collection shape search has, and deliberately so: a result
// says where an item is and what that collection is called, and a client
// navigating from there gets everything else from the collection's own endpoints.
type SearchCollectionRef struct {
	ID   uuid.UUID
	Name string
}

// SearchCollection is the collection projection this feature returns from a
// search.
//
// System collections take part in search: a search for "unsorted" should find the
// Unsorted collection. A result therefore reports only what identifies it and
// names it. type and system_key are not carried here, because a search result is
// a collection to navigate to rather than one to classify, and GET /collections
// is where a client reads how a collection came to be.
//
// Score is the relevance score the row was ordered by, as for SearchSavedItem.
type SearchCollection struct {
	ID    uuid.UUID
	Name  string
	Score float64
}
