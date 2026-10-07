package enrichment

const (
	// CodeSavedItemNotFound is the code recorded when a background task's saved
	// item no longer exists. It matches the code the synchronous endpoint uses for
	// the same situation, so both paths report one situation with one code.
	//
	// It is a code rather than a bare sentinel because the repository constructs
	// it, and it follows the rule that the lowest layer that knows the meaning
	// builds the error. The task handler reads the sentinel from the same file
	// and decides what a missing item means for a retry.
	CodeSavedItemNotFound = "SAVED_ITEM_NOT_FOUND"
)
