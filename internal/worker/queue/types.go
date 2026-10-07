package queue

import (
	"github.com/google/uuid"
)

// EnrichSavedItemPayload is the whole body of an enrichment task.
//
// It carries the saved item's id and nothing else. The URL is deliberately not
// included even though the enqueueing side already has it: the URL in the
// database is the Saved Item's real data, and a queued copy of it would be a
// second value that could disagree with the row. The worker reads the URL from
// the row when it runs, which also means an item saved a moment ago is enriched
// with what was actually stored.
//
// The metadata is not included for the same reason, and one more: the payload
// must not become a second input that enrichment can be run from, or "always
// re-reads the current row" stops being a property of the design.
type EnrichSavedItemPayload struct {
	SavedItemID uuid.UUID `json:"saved_item_id"`
}

// NewEnrichSavedItemPayload builds the payload for one saved item.
func NewEnrichSavedItemPayload(savedItemID uuid.UUID) EnrichSavedItemPayload {
	return EnrichSavedItemPayload{
		SavedItemID: savedItemID,
	}
}

// OrganizeSavedItemPayload is the whole body of an organization task.
//
// It carries the saved item's id and its owner, and nothing else. The id names
// the item; the owner is what makes the task's claim about it checkable, because
// collections are owner-scoped and the worker has no authenticated user to
// resolve them against.
//
// The platform is deliberately absent even though the enrichment that scheduled
// the task knew it. The platform on the row may have changed since, and a
// re-enrichment may have cleared it, so the value that decides where the item
// goes is the one in the database at execution time rather than the one in a
// queued task. The same argument applies to the item's current collection, which
// is why this is a pointer to what the task must verify rather than a decision it
// may act on.
type OrganizeSavedItemPayload struct {
	SavedItemID uuid.UUID `json:"saved_item_id"`
	UserID      uuid.UUID `json:"user_id"`
}

// NewOrganizeSavedItemPayload builds the payload for one saved item.
func NewOrganizeSavedItemPayload(
	savedItemID uuid.UUID,
	userID uuid.UUID,
) OrganizeSavedItemPayload {
	return OrganizeSavedItemPayload{
		SavedItemID: savedItemID,
		UserID:      userID,
	}
}
