package registration

import "time"

const (
	registrationExpiresIn                   = 7 * 24 * time.Hour
	registrationContinuationExpiresIn       = 15 * time.Minute
	verificationCodeExpiresIn               = 5 * time.Minute
	verificationResendCooldown              = 60 * time.Second
	VerificationCodeMaxAttempts       int32 = 5
)

// The per-address PIN budget is not declared here.
//
// It used to be: PinRateLimitMax and PinRateLimitWindow bounded five issuances an
// hour for one address, enforced under the `pin-email` namespace. That budget is
// now shared with the password reset feature and sized from configuration rather
// than from this file, because it is a property of the deployment rather than of
// this feature — the namespace, the policy and the reservation rules all live in
// internal/email, and the limits come from internal/config.
//
// PinIPRateLimitMax below stays here and stays a constant, because it *is* this
// feature's own policy: it is about how often one client address may reach these
// two routes, which is a question about this API rather than about a deployment.
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

// VerifyIPRateLimitMax and VerifyIPRateLimitWindow bound how often one client
// address may reach the PIN verification route.
//
// This is a ceiling on cost, not on guessing. Nothing here is brute-forced: the
// per-code attempt limit already caps guesses at five, and it is enforced by the
// database rather than by anything here. What this bounds is the work a request
// costs even when it guesses wrong — a verification lookup, a code lookup, an
// HMAC comparison, and on failure a write to the attempt counter. An attacker
// holding a valid verification id can otherwise repeat that as fast as the
// endpoint will answer, and none of it is rate limited anywhere else.
//
// It deliberately does not reuse the PIN issuance budget. Issuing a code sends a
// message, which is expensive and which the per-address email budget exists to
// bound; verifying one sends nothing. Charging them to one allowance would mean a
// user who mistypes a PIN could spend the budget that also governs mailing a new
// one, and be unable to recover from the mistake that cost it.
//
// Twenty in ten minutes matches the issuance budget. That is not because the two
// are the same — one is a message, the other is a lookup — but because a genuine
// verification flow makes at most a handful of verify calls, and a shared address
// is very unlikely to reach twenty by accident while an automated flood will.
//
// **These are initial engineering estimates, not production-validated
// thresholds.** No deployment has been selected and no traffic has been observed.
// Revisit once this budget has run against real traffic and real shared addresses.
const (
	VerifyIPRateLimitMax    = 20
	VerifyIPRateLimitWindow = 10 * time.Minute
)

// verifyIPRateLimitNamespace groups this budget's counters in Redis.
//
// It is separate from pinIPRateLimitNamespace because verification and issuance
// are different acts with different costs, and sharing one would let a caller
// spend the issuance allowance simply by verifying, or the verification allowance
// by asking for codes. It is also separate from the login budgets for the same
// reason those are separate from each other: reaching the verify route is not an
// authentication attempt, and an exhausted registration budget must not lock a
// user out of signing in with what they already have.
const verifyIPRateLimitNamespace = "verify-ip"

// FinalizeIPRateLimitMax and FinalizeIPRateLimitWindow bound how often one client
// address may reach the manual finalization route.
//
// This is the only route in this feature that performs an Argon2id derivation,
// which is why it is the tightest of the three. The shared PasswordWorkLimiter
// bounds how many derivations run at once across the whole process, but that is a
// capacity limit and not a rate limit: it stops the process being overrun, and it
// does not stop one caller from occupying every slot indefinitely. Without this,
// an attacker holding a valid continuation token could keep the limiter's slots
// busy and turn every legitimate registration, login and password reset into a
// 503 until they gave up.
//
// Ten in fifteen minutes is set where a genuine user cannot reach it — one
// finalization completes a registration, and a user who mistypes their display
// name may retry a couple of times — while still being far below what occupying a
// shared capacity pool continuously requires. The window is longer than the other
// two because a single finalization is the most expensive request in the flow and
// the budget should therefore stay generous per unit of work.
//
// **These are initial engineering estimates, not production-validated
// thresholds.** No deployment has been selected and no traffic has been observed.
const (
	FinalizeIPRateLimitMax    = 10
	FinalizeIPRateLimitWindow = 15 * time.Minute
)

// finalizeIPRateLimitNamespace groups this budget's counters in Redis.
//
// It is separate from verifyIPRateLimitNamespace even though the two routes are
// adjacent steps of one registration. A caller who verifies five wrong PINs has
// spent a verification budget and must not thereby be unable to finalize, and a
// caller exhausting finalization must not be able to spend verification attempts
// through the same counter. Two steps of one flow are still two acts.
//
// It is separate from the login budgets because finalizing is not authentication.
// The registration continuation token is a capability to create an account, not
// a credential to present, and an exhausted registration budget must not lock a
// user out of signing in with a password they already have.
const finalizeIPRateLimitNamespace = "finalize-ip"
