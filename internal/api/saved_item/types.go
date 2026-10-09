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
