package collection

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
