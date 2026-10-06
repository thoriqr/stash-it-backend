package enrichment

const (
	// CodeSavedItemNotFound is returned when the saved item does not exist or
	// belongs to another user. Both cases share this one code and message, so the
	// operation never discloses whether an id exists. It matches the code the
	// collection feature already uses for the same situation.
	CodeSavedItemNotFound = "SAVED_ITEM_NOT_FOUND"
)
