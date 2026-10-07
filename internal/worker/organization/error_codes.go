package organization

import "errors"

var (
	// ErrSavedItemNotFound is returned when the saved item a task names is gone.
	//
	// It is a sentinel rather than an AppError because this is not an API layer:
	// there is no HTTP status to report. A task for a deleted item is finished
	// rather than failed, so the handler discards it instead of retrying — there
	// is nothing on the other side of a retry that could bring a deleted item
	// back.
	ErrSavedItemNotFound = errors.New("saved item no longer exists")

	// ErrSavedItemOwnershipMismatch is returned when the saved item a task names
	// exists but is not owned by the user the task was scheduled for.
	//
	// Unlike a missing item this is treated as a failure rather than a no-op,
	// because it cannot be an ordinary outcome. Every task is scheduled from the
	// enrichment worker for the item's own owner, so a mismatch means the task's
	// claim and the row disagree, and the item is never touched. Retrying cannot
	// repair that, so the handler stops retrying and the condition is visible in
	// the logs.
	ErrSavedItemOwnershipMismatch = errors.New(
		"saved item does not belong to the task's user",
	)

	// ErrCollectionNameTakenByReservedName is returned when the platform's name is
	// already held by a collection that cannot be the target.
	//
	// It exists for the one case a name lookup cannot resolve: a platform whose
	// display name collides with Unsorted. Organization refuses to file into that
	// collection, because moving an item into Unsorted is a no-op and the item
	// would appear to have been organized while nothing happened.
	ErrCollectionNameTakenByReservedName = errors.New(
		"platform collection name is reserved by the unsorted collection",
	)
)
