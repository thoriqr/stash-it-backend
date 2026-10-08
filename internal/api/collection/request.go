package collection

import "github.com/google/uuid"

// DeleteCollectionRequest states what should happen to the saved items currently
// in a collection that is being deleted.
//
// Both fields are always present and always mean the same thing. The shape does not
// vary by action: the rule about which combination is valid is stated below and
// enforced by the service, so a caller reading this type sees one contract rather
// than two shapes they have to infer from each other.
//
// Neither field carries a `validate` constraint on purpose, for the same reason
// PutSavedItemIntoCollectionRequest does not: the service owns these rules and
// answers with INVALID_COLLECTION_DELETE_ACTION and INVALID_COLLECTION_DELETE_TARGET
// so a caller can tell an incoherent request apart from a malformed one. Tags here
// would pre-empt that and collapse every case into a generic VALIDATION_ERROR.
// ListCollectionsRequest is the query for GET /collections.
//
// The cursor field carries no `validate` rule beyond its length. It is an opaque
// token bound here and nowhere else: decoding it and deciding whether its contents
// are meaningful belong to the service, which owns those rules for every cursor it
// issues. A tag that tried to inspect the payload here would be a second
// implementation of them, in a different place, that could disagree.
//
// max=512 is the one bound that matters on this field. It is what stops a client
// making the server base64-decode an arbitrarily large string, and it matches the
// bound internal/api/auth/session already applies to its opaque refresh token.
type ListCollectionsRequest struct {
	// Sort is the requested order. Absent means newest first.
	Sort CollectionSort `query:"sort" validate:"omitempty,oneof=newest oldest name" example:"newest"`

	// Limit is the number of collections to return. Absent means the default, and a
	// value above the maximum is clamped rather than rejected.
	Limit int `query:"limit" validate:"omitempty,min=1,max=50" example:"20"`

	// Cursor resumes a listing from where the previous page ended. Absent starts at
	// the beginning, with Unsorted first.
	Cursor string `query:"cursor" validate:"omitempty,max=512" example:"eyJ2IjoxLCJzIjoibmV3ZXN0In0"`
}

type DeleteCollectionRequest struct {
	// SavedItemsAction is what to do with the collection's saved items.
	//
	// "delete" removes them along with the collection. "move" files them into
	// target_collection_id first, and then removes the collection. Both are
	// required: the request must say which, because the two are not variants of a
	// single outcome and one of them discards saved items.
	SavedItemsAction SavedItemsAction `json:"saved_items_action" enums:"delete,move" example:"delete"`

	// TargetCollectionID is where the saved items go when SavedItemsAction is
	// "move".
	//
	// It must be a collection id and must be null when SavedItemsAction is
	// "delete". Unsorted is a valid target and is named here by its id, like any
	// other collection; there is no separate mode or flag for it, and no fallback
	// to it. A target that does not exist is an error rather than a substitution.
	TargetCollectionID *uuid.UUID `json:"target_collection_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
}

type PutSavedItemIntoCollectionRequest struct {
	// CollectionName is the display name of the user collection to file the saved
	// item under. The collection is created if it does not exist yet. Names are
	// matched case-insensitively with surrounding whitespace ignored, and a name
	// already used by a system collection is rejected.
	//
	// This field carries no `validate` constraint on purpose. The service owns
	// collection name rules and answers with the INVALID_COLLECTION_NAME code, so
	// a constraint here would pre-empt that and collapse both failure cases into a
	// generic VALIDATION_ERROR.
	CollectionName string `json:"collection_name" example:"Wishlist"`
}
