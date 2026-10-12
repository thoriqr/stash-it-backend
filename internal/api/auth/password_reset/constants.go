package password_reset

import "time"

const (
	passwordResetExpiresIn    					= 15 * time.Minute
	verificationCodeExpiresIn 					= 5 * time.Minute
	passwordResetContinuationExpiresIn 	= 15 * time.Minute
	verificationResendCooldown        	= 60 * time.Second
	VerificationCodeMaxAttempts int32 	= 5
)

// RequestIPRateLimitMax and RequestIPRateLimitWindow bound how often one client
// address may open or look up a password reset.
//
// This endpoint is the account-probing surface. It is the one place a caller can
// learn something about which addresses are registered, so it is where that
// budget has to sit.
//
// Ten in ten minutes is half what PIN issuance is allowed, and deliberately so.
// A reset request sends no message and does no password work: for an address
// nobody holds it is one indexed lookup and nothing else. That makes it cheap
// enough to probe in bulk, which is exactly why it needs a ceiling a bulk probe
// reaches quickly and a person does not.
//
// It is not tighter than that. People share addresses — a household, an office,
// a carrier's CGNAT — and a false positive here is softer than it looks: the
// caller is told to wait, nothing is deleted, and the reset itself lives fifteen
// minutes, so ten requests cannot be spent before a first attempt has expired.
//
// **These are initial engineering estimates, not production-validated
// thresholds.** No deployment has been selected and no traffic has been
// observed. Revisit both values against real traffic and real shared addresses
// before this is relied on.
const (
	RequestIPRateLimitMax    = 10
	RequestIPRateLimitWindow = 10 * time.Minute
)

// requestIPRateLimitNamespace groups the reset-request counters in Redis.
//
// It names the action rather than the route, so adding another reset route
// later cannot accidentally give it its own budget.
const requestIPRateLimitNamespace = "password-reset-request-ip"

// StatusIPRateLimitMax and StatusIPRateLimitWindow bound how often one client
// address may read a verification's status.
//
// This is the frontend's first validation step — it decides whether the id it was
// just handed resolves — and it is also what tells a caller holding a
// placeholder apart from one holding a real flow. Both are reasons to bound it.
//
// Sixty in ten minutes is three times the PIN issuance budget, and the gap is a
// decision rather than an oversight. This route has no side effect: it reads one
// row and returns a status, where issuance sends a message. It is also the route
// a client re-reads while a flow is open, so a value chosen for issuance's cost
// would refuse a legitimate caller watching its own screen. What is bounded here
// is a single indexed lookup, the cheapest thing any of these routes does.
//
// **These are initial engineering estimates, not production-validated
// thresholds.**
const (
	StatusIPRateLimitMax    = 60
	StatusIPRateLimitWindow = 10 * time.Minute
)

// statusIPRateLimitNamespace groups the verification-status counters in Redis.
//
// It is separate from requestIPRateLimitNamespace because opening a reset and
// reading one are different acts, and one flow's flood must not spend another's
// allowance. It is separate from every registration and login budget for the
// same reason those are separate from each other: reaching this route is not an
// authentication attempt, and an exhausted reset budget must not lock anyone out
// of signing in with what they already have.
const statusIPRateLimitNamespace = "password-reset-status-ip"

// PinIPRateLimitMax and PinIPRateLimitWindow bound how often one client address
// may have a verification code issued to it, across both issuance endpoints.
//
// The value is deliberately identical to registration's equivalent policy. This
// is the same act at the same cost — a code generated, a row written, a message
// sent — and matching it is what makes the two features comparable rather than
// merely similar.
//
// What differs is what stands behind it. Registration pairs this budget with a
// per-address email budget of five an hour, so an address that spends its client
// budget is still bounded by a second dimension. Password reset has no per-address
// budget yet, so this counter is currently the only ceiling on what one address
// can be mailed. That is an argument for keeping the value where it is rather
// than lowering it: a tighter limit here would have no second dimension to fall
// back on, and a false positive would stop issuance outright with nothing else to
// absorb it.
//
// Twenty in ten minutes leaves a shared address room for several people and is
// still far below what a flood needs. **These are initial engineering estimates,
// not production-validated thresholds.**
const (
	PinIPRateLimitMax    = 20
	PinIPRateLimitWindow = 10 * time.Minute
)

// pinIPRateLimitNamespace groups the PIN issuance counters in Redis.
//
// It is one namespace rather than two, and that is the whole point of sharing it
// between /pin and /resend. Both reach the same issuance helper, both invalidate
// the previous code, and both send a message. Naming either route separately
// would let a caller double its allowance by switching between them — and
// switching is free, because neither route requires authentication.
//
// It is separate from statusIPRateLimitNamespace so that a client watching its
// own screen while waiting out the sixty-second cooldown cannot spend the
// allowance that governs mailing a replacement, and separate from every
// registration and login budget so exhausting one flow cannot lock another out.
const pinIPRateLimitNamespace = "password-reset-pin-ip"