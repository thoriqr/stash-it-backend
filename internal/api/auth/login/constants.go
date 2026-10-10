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

// GoogleAuthIPRateLimitMax and GoogleAuthIPRateLimitWindow bound how often one
// client address may reach any of the Google authentication routes: signing in
// with a Google ID token, reading an account link confirmation, and confirming
// one. All three spend this one budget, because they are one flow and a caller
// moving between them is not a caller trying harder.
//
// It is a ceiling on cost, and it guards a different cost than the manual-login
// budget above. Manual login exists to stop a secret being guessed, and each
// attempt there costs an Argon2id derivation. The Google routes have no secret to
// guess — the credential is a signature Google made — but they are still not free
// to serve. Every request verifies a token against Google's published keys, and
// a caller holding one valid token can otherwise cause an unbounded number of
// session writes, account link confirmations and pending registrations by
// repeating a request that succeeds. Nothing else bounds any of that, so this is
// the only ceiling on it.
//
// It sits above the manual-login value deliberately. A Google refusal has no
// fallback for the user the way a PIN refusal does: Google login is one of only
// two ways into the application, and a shared address — a household, an office, a
// carrier's CGNAT — sees everyone sign in at the same predictable moments. Every
// refusal here is a false positive rather than a guess running out of attempts,
// so the value is set where a legitimate burst is comfortably inside budget.
//
// Sixty in ten minutes still caps a single address at roughly eight thousand
// requests a day. **These are initial engineering estimates, not
// production-validated thresholds.** No deployment has been selected and no
// traffic has been observed; both values should be reassessed against real
// traffic and against shared-address false positives before this is relied on.
const (
	GoogleAuthIPRateLimitMax    = 60
	GoogleAuthIPRateLimitWindow = 10 * time.Minute
)

// googleAuthIPRateLimitNamespace groups the per-address counters in Redis for the
// Google authentication routes.
//
// It is deliberately a different namespace from both loginIPRateLimitNamespace
// and loginEmailFailureNamespace. Google login and manual login are two doors
// into the same building, not two rooms in it: someone signing in with Google has
// not done anything that should cost their manual-login budget, and someone
// signing in with a password has not earned the right to spend a Google one.
// Sharing either budget would make one flow's flood lock the other flow out.
const googleAuthIPRateLimitNamespace = "google-auth-ip"

// No per-email budget guards any Google route, and the absence is a decision
// rather than an omission.
//
// The email the flow spends would be the one inside Google's verified claim, and
// the caller does not choose it: reaching this code at all required a token
// Google signed asserting, with email_verified, that its holder controls that
// address. An attacker cannot aim such a budget at a victim's mailbox, so a
// per-email budget here could only ever be spent by the legitimate owner of the
// address it names. Its only effect would be locking that owner out of one of
// two ways in, which is a denial of service and not a protection.

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
