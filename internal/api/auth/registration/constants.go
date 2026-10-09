package registration

import "time"

const (
	registrationExpiresIn                   = 7 * 24 * time.Hour
	registrationContinuationExpiresIn       = 15 * time.Minute
	verificationCodeExpiresIn               = 5 * time.Minute
	verificationResendCooldown              = 60 * time.Second
	VerificationCodeMaxAttempts       int32 = 5
)

// PinRateLimitMax and PinRateLimitWindow are how often one email address may have
// a PIN issued for it, across every verification that address owns.
//
// This is a ceiling on cost, not on convenience. The cooldown already stops a
// client from asking faster than once a minute, and it does so per verification,
// which is exactly the gap this closes: an address is unbounded in how many
// verifications it can hold, so an attacker can open several and take a code from
// each as soon as each cooldown lapses. Five an hour is far above what anyone
// needs to recover from a delayed or misfiled email, while putting a hard ceiling
// of a few dozen messages a day on any single address.
//
// The window is an hour rather than the whole seven-day registration lifetime on
// purpose. Budgeting across the lifetime would punish a user across several days for
// an ordinary string of bad mail days, and the punishment would be indistinguishable
// from a broken product. An hour is short enough that a user who does exhaust the
// budget is never stuck for long, and long enough that a legitimate retry pattern
// never reaches it.
//
// These are values rather than configuration because every other lifetime in this
// feature is a value in this file, and none of them is configurable either. Revisit
// once the limiter has run against real traffic.
const (
	PinRateLimitMax    = 5
	PinRateLimitWindow = time.Hour
)

// pinRateLimitNamespace groups this budget's counters in Redis.
//
// It deliberately names the action rather than the route: CreatePIN and
// ResendVerification both send a message to the same address, so both spend the
// same budget. Naming either route here would let a caller double its allowance
// simply by switching between them.
const pinRateLimitNamespace = "pin-email"

// PinIPRateLimitMax and PinIPRateLimitWindow bound how often one client address
// may reach the PIN endpoints, across both of them.
//
// This is a second, separate dimension from the per-email budget and neither
// replaces the other. The email budget stops one address being mailed repeatedly,
// which an attacker with many addresses defeats; the IP budget stops one address
// reaching the endpoints repeatedly, which an attacker with many proxies defeats.
// Either one alone leaves a way in, so both run.
//
// It counts every request that reaches the routes, including ones that go on to
// be rejected. A limit that only counted successful requests would be free for an
// attacker to burn by asking in ways that cannot succeed, which is the cheapest
// kind of request to make.
//
// Twenty per ten minutes is deliberately looser than the per-email budget, and
// that is the point rather than an oversight. Several people behind one
// corporate NAT, one household, or one mobile carrier's CGNAT share one address,
// and this budget is charged to all of them equally. A tighter limit would block
// legitimate neighbours on the strength of a stranger's activity, so the number
// is set where a shared address is very unlikely to reach it by accident, while
// still being far below what an automated flood needs.
//
// Values rather than configuration, matching every other limit and lifetime in
// this feature. Revisit both this and the email budget once either has run
// against real traffic and real shared addresses.
const (
	PinIPRateLimitMax    = 20
	PinIPRateLimitWindow = 10 * time.Minute
)

// pinIPRateLimitNamespace groups this budget's counters in Redis.
//
// It names the action and not the route, for the same reason the email namespace
// does: both endpoints send a message and must draw on one allowance, so naming
// either route here would let a caller double it by switching endpoints.
const pinIPRateLimitNamespace = "pin-ip"
