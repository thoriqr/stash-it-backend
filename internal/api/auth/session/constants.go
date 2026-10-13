package session

import "time"

const (
	SessionAbsoluteLifetime = 90 * 24 * time.Hour
	SessionIdleLifetime     = 30 * 24 * time.Hour
	AccessTokenLifetime     = 15 * time.Minute
	SessionListDefaultLimit = 20
	SessionListMaxLimit     = 50
)

// RefreshIPRateLimitMax and RefreshIPRateLimitWindow bound how often one
// resolved client address may reach the refresh endpoint.
//
// This is a ceiling on cost, and the cost it guards is not the one its peers
// guard. There is no Argon2id here and nothing leaves the process: a refresh is
// one indexed lookup plus, on success, two row locks, three writes and a commit.
// On its own that is not an argument for a limit.
//
// What makes it one is that rotation hands the caller a successor. A caller
// holding a single stolen token can therefore refresh again and again, always
// presenting the newest token, and never present one that has already been
// replaced — so the reuse detection the refresh path performs never fires
// against it. Each of those requests is legitimate as far as the service can
// tell, and each writes a refresh_tokens row that nothing ever removes. The
// chain is bounded only by the session's absolute lifetime, ninety days, which
// is a long time to be unbounded by rate.
//
// The value is derived from AccessTokenLifetime rather than chosen. A client
// that refreshes only when it must does so once per fifteen minutes, which is
// 0.67 times in ten; sixty in ten therefore leaves room for roughly ninety
// devices on one address refreshing continuously with no burst at all, and about
// thirty of them through a threefold burst. Households, offices and carrier
// NATs all share an address, and refresh is the highest-frequency legitimate
// route in the application — every active device does it automatically, where
// login and Google sign-in are deliberate human actions a handful of times a
// day. Anything tighter would be measuring the wrong thing.
//
// It deliberately matches password-reset-status-ip's sixty in ten minutes. That
// budget exists for the same shape of route: cheap per request, re-read
// repeatedly by a legitimate client, where a value chosen for the expensive
// neighbour instead would refuse a real caller. Sharing the number is a
// judgement that the two routes have comparable legitimate rates. It does not
// share the counter; see refreshIPRateLimitNamespace below.
//
// A refusal here is worse than a refusal anywhere else in the application. This
// endpoint is how a client recovers once its access token expires, and the
// access token lives fifteen minutes. A caller refused here is not merely
// delayed; they are a user waiting to be signed out of their own account.
//
// **These are initial engineering estimates, not production-validated
// thresholds.** No deployment has been selected and no traffic has been
// observed. The assumption this rests on is the number of devices behind a
// shared address, which is not knowable from here; the assumption most likely
// to require raising it is client retry behaviour, since a client that retries
// refresh aggressively multiplies its own baseline and lives outside this
// repository. Reassess both against real traffic before relying on this.
const (
	RefreshIPRateLimitMax    = 60
	RefreshIPRateLimitWindow = 10 * time.Minute
)

// refreshIPRateLimitNamespace groups the refresh counters in Redis.
//
// It is its own namespace, never login-ip and never google-auth-ip, even though
// those routes authenticate too. They are three separate ceilings for the same
// reason the saved-item and collection budgets are separate from each other: a
// caller flooding one route must not spend another route's allowance, and an
// exhausted refresh budget must never become a way to lock a user out of signing
// in with a password or a Google token they still hold. Merging them would let
// one flow's flood take the other doors down with it.
//
// The subject is the client address rather than the refresh token, and
// deliberately so. A token-keyed budget would be defeated by exactly the
// rotation chaining this budget exists to bound: every successor is a new hash,
// so every request would land on a counter of its own.
const refreshIPRateLimitNamespace = "refresh-ip"
