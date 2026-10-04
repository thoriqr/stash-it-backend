package collection

const (
	// CodeInvalidCollectionName is returned when the submitted name is blank
	// after trimming or longer than CollectionNameMaxLength.
	CodeInvalidCollectionName = "INVALID_COLLECTION_NAME"

	// CodeCollectionNameReserved is returned when the name is already held by a
	// system collection. System collection names are reserved so a user cannot
	// shadow them, and a user collection may not be given one either.
	CodeCollectionNameReserved = "COLLECTION_NAME_RESERVED"

	// CodeSavedItemNotFound is returned when the saved item does not exist or
	// belongs to another user. Both cases share this one code and message, so the
	// operation never discloses whether an id exists.
	CodeSavedItemNotFound = "SAVED_ITEM_NOT_FOUND"
)
