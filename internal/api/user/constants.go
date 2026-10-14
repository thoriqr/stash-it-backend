package user

import "time"

// passwordCredentialPrimaryKey is the constraint a duplicate credential loses to.
//
// It is the table's primary key, declared in migrations/000005 as
// `user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE`, which makes
// it the same thing as a unique constraint on user_id and therefore the only
// possible conflict target for CreatePasswordCredential.
//
// Naming it here rather than spelling the string at the point of use keeps the
// mapping from driver error to response code in one place: the SQL names the
// constraint it creates and the code names the constraint it refuses on, and
// those two are the same identifier by construction.
//
// It is matched against pgconn.PgError.ConstraintName rather than against the
// error text, for the reason the collection feature already relies on when it
// maps saved_items_collection_id_fkey.
const passwordCredentialPrimaryKey = "password_credentials_pkey"

// PasswordUserRateLimitMax and PasswordUserRateLimitWindow bound how often one
// authenticated user may reach PUT /users/me/password.
//
// This route performs Argon2id work and needs a budget for the reason
// security.PasswordWorkLimiter does not provide one: that limiter bounds how many
// derivations run at once across the whole process, and a caller holding every
// slot and re-acquiring it on release can hold them indefinitely while never
// tripping a limit that is never exceeded. The reasoning is the one
// registration's FinalizeIPRateLimitMax and password reset's
// FinalizeIPRateLimitMax already state, in the same words, for the routes that
// perform one derivation each.
//
// **The subject is the account, not the client address.** The caller is already
// authenticated, so claims.Subject is available and is the thing ownership is
// checked against on every query this route runs. An address budget would not
// survive a caller changing address, and it would charge a household or an office
// behind one NAT for one person's password work. This is the same reasoning
// search's budget and both saved-item budgets are mounted on. It is deliberately
// unlike finalize-ip and password-reset-finalize-ip, which are per-address because
// those routes are reachable without a credential at all and an address is the
// only subject available before one is presented.
//
// Five in fifteen minutes is half of the 10/15 both single-derivation finalize
// routes use, and the halving is the point rather than an inconsistency. Changing
// a password performs **two** derivations — a Verify of the current one and a Hash
// of the replacement — where creating a first one performs one. password_reset's
// FinalizeIPRateLimitMax is set to 10/15 precisely because "both routes perform
// one derivation per request and cost the same to serve, so matching them is what
// makes the two features comparable"; that principle is conditional on the cost
// matching, and here it does not.
//
// Five is still a value a legitimate user cannot reach. A first password is set
// once. A password is changed rarely, and the one realistic burst — someone
// reacting to a password they believe was seen — is two or three requests.
//
// The fifteen-minute window is copied from those same two routes, for the reason
// registration's constants give: a single request here is among the most expensive
// in the application, so the budget stays generous per unit of work. A caller who
// does exhaust it is refused for at most that window.
//
// **There is no release on success, and no failure-only counter.** login's
// login-email budget charges optimistically and releases when the work succeeds, so
// that a mistyped password costs nothing. The situations are inverted here: at
// login a success is the common case and a failure is the exception, whereas a
// *failed* password change is exactly what this budget exists to bound, because the
// change branch verifies a real secret. An unreleased charge is also the direction
// releaseEmailFailure's own comment calls the safe one.
//
// **These are initial engineering estimates, not production-validated values.**
// No deployment has been selected and no traffic has been observed. Revisit
// against real traffic, and against real shared addresses if an address budget is
// ever added alongside this one.
const (
	PasswordUserRateLimitMax    = 5
	PasswordUserRateLimitWindow = 15 * time.Minute
)

// passwordUserRateLimitNamespace groups these counters in Redis.
//
// It is its own namespace, and it takes no part in any other flow's budget. A
// namespace is one flow's allowance, the limiter fails closed, and a shared
// counter would let one exhausted flow take another offline with it: a user who
// had just used up their password budget must still be able to sign in, refresh,
// register and reset, and each of those has its own counter for exactly that
// reason.
//
// The trailing "-user" is the same shape search's and the saved-item budgets use
// (search-user, saved-item-create-user, saved-item-enrich-user): it names the
// subject dimension, so the key alone says which axis was charged.
const passwordUserRateLimitNamespace = "user-password-user"

// The two profile routes carry no budget, and their absence is a decision.
//
// GET /users/me and PATCH /users/me spend one indexed users read, one probe
// against password_credentials and one read of auth_identities. Neither derives a
// password, fetches a URL, writes to a queue, nor holds a lock or cursor that
// outlives the request. A rename in particular *writes*, and that alone is not
// enough to justify a limit here: DELETE /saved-items/:id and
// DELETE /collections/:id both write and both are deliberately unlimited. The
// distinction this repository actually draws is in saved_item/routes.go, where the
// save route is budgeted because it *arranges* unbounded downstream work by
// enqueuing an enrichment task. A rename arranges nothing.
//
// The stronger argument is availability rather than cost. middleware.UserRateLimit
// fails closed — an unreachable Redis is a refusal, not permission — so budgeting
// these two routes would mean that during a Redis outage the application could not
// render a profile or rename itself. That is a real loss bought for no protected
// resource, and this repository already accepts the fail-closed cost where it
// matters instead, on POST /auth/refresh.
//
// ListAuthIdentitiesForUser still runs without an index on auth_identities.user_id,
// which was deferred rather than forgotten, so that read scans the table. It scans
// one account's identities — a handful of rows — and its cost is not proportional
// to anything the caller controls, which is the property the search budget exists
// to bound. It is recorded here so the deferral is a visible decision rather than
// an oversight.
