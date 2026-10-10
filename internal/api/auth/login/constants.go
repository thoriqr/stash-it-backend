package login

import "time"

const AccountLinkConfirmationLifetime = 15 * time.Minute

// LoginIPRateLimitMax and LoginIPRateLimitWindow bound how often one client
// address may reach the manual login endpoint.
//
// This is a ceiling on cost rather than on convenience. Every attempt on the
// endpoint costs an Argon2id derivation of tens of milliseconds and 64 MiB of
// memory whether or not it succeeds, so an unlimited endpoint is an
// amplification point: cheap to call, expensive to serve. Thirty in ten minutes is
// well inside what one machine absorbs while still capping a single address at
// roughly forty thousand attempts a day, which is far below anything a spraying
// tool needs to be useful.
//
// It sits above the equivalent PIN limit on purpose. People share addresses — a
// household, an office, a mobile carrier's CGNAT — and they do so most heavily
// at predictable moments, when everyone logs in at once. A login false positive
// is worse than a PIN false positive: the user cannot authenticate at all, and
// there is no second way in. This value leaves room for a shared address to log
// several people in together, and should be reassessed against real traffic.
const (
	LoginIPRateLimitMax    = 30
	LoginIPRateLimitWindow = 10 * time.Minute
)

// loginIPRateLimitNamespace groups the per-address counters in Redis.
//
// It names the action rather than the route so that adding another login route
// later cannot accidentally give it its own budget.
const loginIPRateLimitNamespace = "login-ip"

// LoginEmailFailureLimit and LoginEmailFailureWindow bound how many failed
// authentications one email address may have within the window.
//
// The IP limit above is defeated by distributing requests across addresses, which
// is what a credential-stuffing list does, so an address that is spread across a
// thousand IPs is never near its per-IP ceiling. This budget is what still holds
// when that happens: every guess against one account spends the same budget
// wherever it came from.
//
// Ten in fifteen minutes is roughly three times what someone mistyping their own
// password needs, and it bounds an attacker to forty guesses an hour against one
// account regardless of how many addresses they hold. Five would halve that at
// the cost of locking real users out; twenty would double it at the cost of a
// full hour of lockout after twenty mistakes.
//
// This is a trade, not a solved problem. An attacker who knows an address can
// deliberately exhaust this budget and lock that account out for up to the window.
// The window is kept short precisely so that recovery is quick, and
// LoginEmailFailureWindow is the single value to revisit if lockouts are seen.
const (
	LoginEmailFailureLimit  = 10
	LoginEmailFailureWindow = 15 * time.Minute
)

// loginEmailFailureNamespace groups the per-address failure counters in Redis.
//
// Only failures are counted against it, and it is deliberately a different
// namespace from the per-IP budget: the two guard different dimensions and
// merging them would mean exhausting one exhausted the other.
const loginEmailFailureNamespace = "login-email"
