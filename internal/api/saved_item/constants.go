package saved_item

import "time"

const (
	// CollectionSystemKeyUnsorted is the stable identity of the Unsorted
	// collection, the one collection a user may never delete.
	//
	// It is compared as an exact value and never derived from the display name.
	// Unsorted is identified by this key because the name is free text a client
	// renders, while the key is what the schema requires every system collection
	// to carry and what collections_system_key_unique makes unique per user.
	//
	// The same identity is defined by internal/api/collection and by
	// internal/worker/organization. They are separate features on purpose and do
	// not import one another, so each states the value it depends on, and the
	// three must agree: the key is the one definition of which collection is
	// Unsorted.
	//
	// A collection's type is deliberately not part of this. 'system' describes who
	// created a collection, not who owns it and not what it is, so a system
	// collection is one the user may delete exactly like one of their own.
	CollectionSystemKeyUnsorted = "unsorted"
)

// CreateUserRateLimitMax and CreateUserRateLimitWindow bound how often one
// authenticated user may save a URL.
//
// The request itself is cheap — a lookup for the Unsorted collection, one insert,
// one queue write — and the fetch of the page is deliberately left to the worker,
// where `WorkerConcurrency` bounds it. What this budget bounds is therefore not
// the save but what a save **arranges**: every save enqueues an enrichment task,
// nothing deduplicates them, and the queue is not capped. Left unbounded, one
// account can grow the queue without limit and the work it represents lands on the
// worker that every other user's saves also depend on.
//
// It is keyed on the authenticated user rather than on a client address, because
// the rows it creates are per account and the caller chooses its own network path.
// An address budget would not survive a caller changing address, and would charge
// a household or an office behind one NAT for each other's activity.
//
// Sixty in ten minutes sits well above what saving is for. A person saving their
// own reading list does not approach it, and it still caps sustained abuse at one
// task every ten seconds per account. It is looser than the enrichment budget
// because the work it schedules is cheaper per call and lands on a bounded worker
// rather than in an API handler.
//
// **These are initial engineering estimates, not production-validated
// thresholds.** No deployment has been selected and no traffic observed. Revisit
// against real traffic once there is some.
const (
	CreateUserRateLimitMax    = 60
	CreateUserRateLimitWindow = 10 * time.Minute
)

// createUserRateLimitNamespace groups the save counters in Redis.
//
// It is its own namespace, separate from the enrichment budget, because the two
// routes cost different things and one flow's flood must not spend another's
// allowance: this one spends a database insert and a queue write, while
// `POST /saved-items/:id/enrich` spends an outbound fetch in the API process. A
// user who has been throttled on one must still be able to use the other.
//
// It is separate from every auth namespace because saving is not an authentication
// attempt, and an exhausted save budget must never lock anyone out of signing in
// with a password they already have.
const createUserRateLimitNamespace = "saved-item-create-user"
