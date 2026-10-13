package enrichment

import "time"

// EnrichUserRateLimitMax and EnrichUserRateLimitWindow bound how often one
// authenticated user may ask for a saved item's metadata to be refreshed.
//
// This is the only route in the application that performs an outbound fetch **in
// the API process**. Everything else that reads a page does it in the worker, where
// `WorkerConcurrency` bounds it. Here the fetch runs inline with the request, so
// each call holds a handler and a database connection for as long as
// `security.OutboundFetchTimeout` allows — up to ten seconds. That is why this
// budget exists, and why it is the tightest one in the application: the scarce
// resource is shared by every account, so one user looping this route degrades
// everyone else's requests to it.
//
// It is keyed on the authenticated user rather than on a client address, because
// the work is per account and the caller chooses its own network path. An address
// budget would not survive a caller changing address, and would charge a
// household or an office behind one NAT for each other's activity.
//
// Ten a minute sits where a person cannot reach it by re-enriching deliberately:
// refreshing an item's metadata is something a user does when a page looks wrong,
// not something they do repeatedly. It still permits a genuine user fixing several
// items in a row, and caps sustained abuse at roughly one outbound fetch every six
// seconds per account.
//
// **These are initial engineering estimates, not production-validated
// thresholds.** No deployment has been selected and no traffic observed. Revisit
// against real traffic once there is some.
const (
	EnrichUserRateLimitMax    = 10
	EnrichUserRateLimitWindow = time.Minute
)

// enrichUserRateLimitNamespace groups the enrichment counters in Redis.
//
// It is its own namespace, named for the operation rather than for the route, so
// that the saved-item creation budget is a separate counter. The two routes cost
// different things: this one spends an outbound fetch in the API process, while
// `POST /saved-items` spends a database insert and a queue write and pushes its
// fetch to the worker. One shared allowance would let a caller saturating one
// starve the other, and a user refreshing metadata must not thereby be unable to
// save something new.
//
// It is separate from every auth namespace because reaching this route is not an
// authentication attempt: an exhausted enrichment budget must never lock anyone
// out of signing in, and one route's flood must not spend another's allowance.
const enrichUserRateLimitNamespace = "saved-item-enrich-user"
