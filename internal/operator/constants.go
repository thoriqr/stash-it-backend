package operator

import "time"

// Statuses a report may hold. They mirror the schema's CHECK, which is the one place
// that decides what a status may be.
const (
	StatusNew     = "new"
	StatusPlanned = "planned"
	StatusClosed  = "closed"
)

// Statuses lists every status in the order the filter shows them.
//
// The order is the lifecycle order rather than alphabetical, because that is how a
// person reads the queue: what is new, what was accepted, what is finished.
var Statuses = []string{StatusNew, StatusPlanned, StatusClosed}

// DefaultLimit and MaxLimit bound one page of reports.
//
// The page is read by one person on a laptop, so it is deliberately small. A limit
// above the maximum is clamped rather than rejected: this is a page, not an API, and
// the useful answer to an oversized request is a page rather than an error a browser
// cannot act on.
const (
	DefaultLimit = 20
	MaxLimit     = 100
)

// SecretHeader carries the shared secret that guards this page.
//
// The page is not public: it is bound to loopback on the VPS and reached over an SSH
// tunnel, or bound to a development address locally. The header exists for the case
// where neither is true — a page bound to a reachable address by mistake — so that
// being reachable is not the same as being readable. It is the same shape session
// cleanup and client releases use.
const SecretHeader = "X-Operator-Secret"

// Now is the clock the page stamps its footers with.
//
// A function rather than a direct time.Now call so a template test can render a fixed
// moment. It returns local time because the only reader is a person looking at when a
// report arrived.
func Now() time.Time {
	return time.Now()
}
