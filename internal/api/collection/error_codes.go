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

	// CodeCollectionNotFound is returned when the collection being deleted does
	// not exist or belongs to another user. Both cases share this one code and
	// message, for the same non-disclosure reason as CodeSavedItemNotFound.
	CodeCollectionNotFound = "COLLECTION_NOT_FOUND"

	// CodeUnsortedCollectionProtected is returned when the collection being
	// deleted is the Unsorted one. Unsorted is identified by its system_key, never
	// by its display name and never by its type, and it is the only collection that
	// cannot be deleted: every save lands there, so removing it would leave saving
	// with nowhere to go.
	CodeUnsortedCollectionProtected = "UNSORTED_COLLECTION_PROTECTED"

	// CodeInvalidCollectionDeleteAction is returned when saved_items_action is
	// missing or is neither 'delete' nor 'move'. Nothing is defaulted: the caller
	// states what should happen to the saved items, and guessing on its behalf
	// would risk deleting content nobody asked to delete.
	CodeInvalidCollectionDeleteAction = "INVALID_COLLECTION_DELETE_ACTION"

	// CodeInvalidCollectionDeleteTarget is returned when target_collection_id does
	// not match the chosen action: it must be absent for 'delete' and present for
	// 'move'. A target alongside 'delete' is a contradiction rather than a default,
	// and a missing one alongside 'move' leaves nowhere to put the items.
	CodeInvalidCollectionDeleteTarget = "INVALID_COLLECTION_DELETE_TARGET"

	// CodeCollectionDeleteTargetNotFound is returned when the target collection of
	// a 'move' does not exist or belongs to another user. There is deliberately no
	// fallback to any other collection: the caller named one, so the backend
	// executes that or reports that it cannot.
	CodeCollectionDeleteTargetNotFound = "COLLECTION_DELETE_TARGET_NOT_FOUND"

	// CodeInvalidCollectionSort is returned when the requested sort is not one of
	// the supported orders. Nothing is defaulted beyond an absent sort: choosing an
	// order on a caller's behalf would return rows in a sequence they did not ask
	// for, which for a paged listing means a cursor that means something different
	// from what they expected.
	CodeInvalidCollectionSort = "INVALID_COLLECTION_SORT"

	// CodeInvalidCursor is returned when a cursor cannot be used. That covers every
	// way a token can fail: malformed base64 or JSON, an unsupported payload version,
	// a missing or contradictory field, and a cursor issued for a different sort.
	// They share one code because they all mean the same thing to a caller, start
	// over without a cursor, and telling them apart in the message would only make
	// the token's internal shape easier to probe.
	CodeInvalidCursor = "INVALID_CURSOR"

	// CodeCollectionNotEmpty is returned when the collection could not be removed
	// because a saved item was filed into it after the operation had already dealt
	// with the items it found. The database's ON DELETE RESTRICT constraint is what
	// detects this, so nothing was deleted: the whole transaction rolled back and
	// the collection and its items are as they were.
	CodeCollectionNotEmpty = "COLLECTION_NOT_EMPTY"
)
