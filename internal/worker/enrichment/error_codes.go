package enrichment

import "errors"

// ErrSavedItemNotFound is returned when the saved item a task names is gone.
//
// It is a sentinel rather than an AppError because there is no HTTP status to
// report here: this is not an API layer and the worker has no response to shape.
// It is a plain value because the important thing about it is what the caller
// does next, and that is a queue decision. A task for a deleted item is
// finished, not failed, so it must be discarded rather than retried — there is
// nothing on the other side of the retry that could make the item come back.
var ErrSavedItemNotFound = errors.New("saved item no longer exists")
