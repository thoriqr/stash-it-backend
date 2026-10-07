package organization

const (
	// CodeSavedItemNotFound is the code recorded when a task names a saved item
	// that no longer exists. It matches the code the enrichment worker uses for
	// the same situation, so one situation has one code across both workers.
	CodeSavedItemNotFound = "SAVED_ITEM_NOT_FOUND"

	// CodeSavedItemOwnershipMismatch is the code recorded when a task names a
	// saved item that exists but belongs to a different user.
	CodeSavedItemOwnershipMismatch = "SAVED_ITEM_OWNERSHIP_MISMATCH"

	// CodeCollectionNameReserved is the code recorded when the platform's name
	// cannot be a target collection. It matches the code the collection API uses
	// for the same situation, so one situation has one code across both writers.
	CodeCollectionNameReserved = "COLLECTION_NAME_RESERVED"
)

// CollectionSystemKeyUnsorted is the identity of the collection a saved item is
// created into, and the only one automatic organization is allowed to move an
// item out of.
//
// It is matched as a system_key rather than as a display name because system_key
// is a collection's stable identity, and because collections_system_key_check
// gives every user collection a NULL one. That is what lets a collection the user
// named "Unsorted" themselves be told apart from the system collection of the
// same name.
const CollectionSystemKeyUnsorted = "unsorted"
