# Auth sessions and tokens

Purpose of this document: record how sessions, access tokens and refresh tokens
relate to each other, which guarantees are enforced by the database and which are
only a convention, what has been decided on purpose, and what is still open. It
is the reference for the session feature; `PROGRESS.md` records that it exists
and what changed when.

`AGENTS.md` states the short version of the policy — access tokens carry `sub` and
`sid`, refresh tokens are opaque and stored hashed, reuse of a rotated token
revokes the session. That is enough to avoid breaking it. This document is for
the questions that short version deliberately does not answer: why the lock is on
two rows, which failure each constraint is actually preventing, and what the
feature does _not_ guarantee.

## What the feature owns

`internal/api/auth/session` is the only feature that writes `sessions` or
`refresh_tokens`. It exposes two interfaces, and they have deliberately different
audiences:

| Interface        | Method                                                           | Consumed by                               |
| ---------------- | ---------------------------------------------------------------- | ----------------------------------------- |
| `SessionCreator` | `CreateSession`                                                  | `login` (twice) and `registration` (once) |
| `SessionService` | `RefreshToken`, `Logout`, `ListSessions`, `RevokeSessionForUser` | its own handler only                      |

`SessionCreator` is narrowed to one method because its callers want a session and
nothing else. `SessionService` is the HTTP surface. The separation is what lets
another feature mint a session without inheriting the ability to revoke one.

Three credentials are involved and they are not interchangeable:

- An **access token** is an HS256 JWT carrying `sub` (the user) and `sid` (the
  session), living 15 minutes. It is **stateless**: nothing in
  `middleware.Auth` reads the database, so verifying it proves a signature and
  nothing about the session's current state.
- A **refresh token** is 32 bytes from `crypto/rand`, base64url-encoded. Only its
  SHA-256 hash is stored. It is single-use and rotates on every use; it carries no
  expiry of its own, so its usable life is bounded by the session's windows
  rather than by a column on its row.
- The **session row** is the authority. `revoked_at`, `absolute_expires_at` and
  `last_activity_at` on it are what the refresh path consults under a lock.

The relationship: the access token is a bearer of the session's identity, the
refresh token is how that identity is renewed, and the session row is what decides
whether renewal is allowed.

### Identifiers

| Identifier                       | Purpose                                                                   |
| -------------------------------- | ------------------------------------------------------------------------- |
| `sessions.id` (`uuidv7()`)       | names the session; the access token's `sid` claim                         |
| `sessions.user_id`               | owner; never taken from a request body or query                           |
| `sessions.installation_id`       | client-supplied, from `X-Installation-ID`; a device, not an authorization |
| `sessions.platform`              | `X-Platform`; describes the client, never a Saved Item's `platform`       |
| `sessions.device_name`           | `X-Device-Name`                                                           |
| `sessions.user_agent`            | `User-Agent`                                                              |
| `refresh_tokens.id` (`uuidv7()`) | the row; `replaced_by` points at the successor's row id                   |
| `refresh_tokens.token_hash`      | the lookup key; unique                                                    |

`installation_id` in particular is **not** an authorization mechanism. It is a
client-supplied label that helps a person recognise a device in a list; the owner
check is `user_id`, and nothing keys off it.

### Routes

| Route                                     | Auth                  | Service method         |
| ----------------------------------------- | --------------------- | ---------------------- |
| `POST /auth/refresh`                      | none (token body)     | `RefreshToken`         |
| `POST /auth/logout`                       | Bearer                | `Logout`               |
| `GET /auth/sessions`                      | Bearer                | `ListSessions`         |
| `DELETE /auth/sessions/:id`               | Bearer + path id      | `RevokeSessionForUser` |
| `POST /internal/maintenance/sessions/cleanup` | `X-Cleanup-Secret` | `CleanupSessions`      |

Auth is applied per route with `middleware.Auth(verifier)`, not with
`router.Use`. `POST /auth/refresh` is unauthenticated because the refresh token in
the body _is_ the credential; nothing else about the request is trusted, and in
particular no user or session id is accepted from it.

`POST /auth/refresh` additionally carries a per-client-address budget — see
"Rate limiting refresh" below.

The cleanup route is deliberately **not** mounted under `/auth`. Every `/auth`
route either carries a Bearer token or takes its credential in the body, and an
operator-triggered maintenance call holds neither; putting it in that namespace
would stop the prefix meaning what it means. `session.MaintenanceRoutes` registers
it on its own group, which is the arrangement `collection` already uses for its
two resources.

## Lifecycle

### Creation

`service.CreateSession` generates a refresh token, hashes it, and computes
`absolute_expires_at` as `time.Now() + 90 days` **in Go**. The repository then
opens one transaction that inserts the `sessions` row and the `refresh_tokens`
row, and commits before returning. If either insert fails, neither survives.

Sessions are created from exactly three places:

- `login.LoginManual`
- `login.loginWithUser` — reached by `LoginGoogle` for an already-linked
  identity, and by `ConfirmAccountLink` after the link has committed
- `registration.FinalizeSocialRegistration`

**Manual registration finalization creates no session.**
`FinalizeManualRegistrationResult` carries only the user id, email and display
name, so the client signs in through `POST /auth/login` afterwards. Social
finalization does mint one and returns both tokens. The two finalize paths are
therefore not interchangeable from a client's point of view, and the difference is
in the result types rather than in a flag.

### Storage and hashing

The raw token is returned to the client exactly once and is never persisted.
`security.HashToken` is SHA-256 over the token, base64url-encoded. A plain hash is
appropriate here specifically because the input is 256 bits of CSPRNG output:
there is no dictionary to attack, so a keyed construction would buy nothing.

`refresh_tokens_token_hash_idx` is UNIQUE. It is the lookup index for refresh and
the guard that makes the rollback test possible.

### Rotation

`repository.RefreshToken` runs one transaction:

```
lock the token row and its session row   (GetRefreshTokenWithSessionForUpdate)
  -> refuse if the session is revoked            -> SESSION_REVOKED
  -> refuse if this token was already replaced   -> revoke the session, commit, SESSION_REVOKED
  -> refuse if the session passed its absolute expiry -> revoke the session, commit, SESSION_EXPIRED
  -> refuse if the session passed its idle window    -> revoke the session, commit, SESSION_EXPIRED
  insert the successor token
  set replaced_by on the consumed token
  stamp last_activity_at = NOW()
commit
```

The access token is minted **after** the commit, from the `user_id` and
`session_id` the locked row returned. No caller-supplied value reaches it.

### Reuse detection

A token with `replaced_by` set is one that has already been spent. Presenting it
again revokes the **whole session**, not just the token, and the revocation is
committed before `SESSION_REVOKED` is returned. That ordering is the point:
returning the error _is_ the notice to the legitimate holder that something
happened, so it must not be able to arrive before the session actually dies. If
the commit fails, the caller gets `INTERNAL_SERVER_ERROR` instead — a false
"you were revoked" would be a lie about durable state.

### Logout and explicit revocation

Two different contracts, deliberately:

- `RevokeSession` (logout) matches `id AND user_id AND revoked_at IS NULL` and
  returns nothing. Missing, already revoked and another user's are all silent
  successes, so logging out twice is safe and a caller cannot probe for the
  existence of a session id.
- `RevokeSessionForUser` (`DELETE`) adds `RETURNING id`, so no row matched is a
  `404 RESOURCE_NOT_FOUND`. Unknown and foreign are the same answer, which keeps
  it non-enumerating.

`DELETE` is the only route in this feature that takes its target from the request
rather than from the verified token, which is why its ownership predicate carries
the weight it does.

### Listing

`ListSessions` and `CountSessions` share one predicate — owned by the caller and
not revoked — and the page is ordered `last_activity_at DESC, id DESC` with
`LIMIT`/`OFFSET` (see "Session listing ordering" for why the second sort key is
there). They are two statements with no shared snapshot, so `total` can disagree
with the page if a login or logout lands between them.

`is_current` is computed in the handler by comparing each returned id against the
caller's own `sid` claim. It is not stored.

### Expiry

Both windows are constants in `session/constants.go`:

| Constant                  | Value      | Measured from               |
| ------------------------- | ---------- | --------------------------- |
| `SessionAbsoluteLifetime` | 90 days    | session creation            |
| `SessionIdleLifetime`     | 30 days    | `sessions.last_activity_at` |
| `AccessTokenLifetime`     | 15 minutes | access token issue          |

`absolute_expires_at` is written once by `CreateSession` and **never updated**.
Rotation does not extend it, so a session cannot outlive 90 days no matter how
actively it is used.

`last_activity_at` is written **only** by `UpdateSessionActivity`, which is called
**only** from the refresh path. It is a record of _refreshing_, not of request
activity — an authenticated API call does not touch it. The name is honest about
the column and slightly generous about the meaning.

### Revocation, and what it does not do

Revoking a session stops the refresh path immediately: the next refresh re-reads
`sessions.revoked_at` under the lock and answers `SESSION_REVOKED`. Any successor
token minted moments earlier is caught by the same check, so a revoked session
cannot be extended.

**It does not invalidate an access token that was already issued.** Those are
signed JWTs and nothing consults the database to check them, so a token issued
before revocation keeps working until its own 15-minute expiry. Nothing in the
code, the Swagger annotations or `PROGRESS.md` promises otherwise, so this is a
property of the design rather than a gap in it. The exposure window is bounded by
`AccessTokenLifetime`.

## Session cleanup

Nothing removed a dead session before this, so a session refreshed every fifteen
minutes for its full ninety-day lifetime accumulated roughly 8,600 `refresh_tokens`
rows and the `sessions` table only ever grew. `POST
/internal/maintenance/sessions/cleanup` removes them.

### The predicate

```sql
revoked_at IS NOT NULL
OR absolute_expires_at <= NOW()
OR last_activity_at < NOW() - <SessionIdleLifetime>
```

These are the refresh path's own three conditions, read from the row rather than
decided a second time. `RefreshToken` refuses a revoked session, a session past
`absolute_expires_at`, and a session past the idle window, so **every row cleanup
matches is a row refresh already refuses**. Cleanup therefore cannot shorten a
session that is still usable, which is the property the whole feature rests on.

The comparison is exact about which side is inclusive, and the asymmetry is
deliberate. The refresh path asks `time.Now().After(absolute_expires_at)`, which
is `absolute_expires_at <= now`, and asks whether the idle gap *exceeds* the
window, which is `last_activity_at < now - window`. Writing `<=` on the idle side
too would delete a session sitting exactly on the boundary that refresh would
still accept.

`SessionIdleLifetime` is the only source of the duration. The repository takes it
as a parameter and binds it as an interval; the literal `30 days` does not appear
in any query file, so the cleanup and the refresh path cannot drift apart.

All three conditions are evaluated against `NOW()`, the database clock. The idle
window is the comparison that matters most, and `last_activity_at` is written by
PostgreSQL, so pairing it with the database's own timestamp is the consistent
choice. **This is a deliberate difference from the runtime path**, which compares
a database-written `last_activity_at` against Go's `time.Now()` — the open item
recorded below. The two only disagree if the API host and the database clocks
diverge by more than thirty days, which is not a state that occurs.

### Only the parent row is deleted

The statement removes rows from `sessions` and nothing else.
`refresh_tokens.session_id → sessions(id) ON DELETE CASCADE` takes each session's
whole token chain with it in the same statement.

**No statement anywhere deletes an individual refresh token while its session
survives.** That is the constraint "Pruning refresh tokens would silently disable
reuse detection" below, and it is the reason cleanup is built this way. Deleting
the parent deletes the chain atomically and cannot leave a predecessor pointing at
nothing; deleting one token out of a chain would.

### Batching

One execution runs the bounded statement repeatedly:

| Constant                    | Value | What it bounds                                    |
| --------------------------- | ----- | ------------------------------------------------- |
| `SessionCleanupBatchSize`   | 500   | rows locked and cascaded by one statement          |
| `SessionCleanupMaxBatches`  | 100   | how many statements one request may run            |

Each batch is its own statement and therefore its own transaction. That is
deliberate: a failure in a later batch leaves the earlier ones committed, and the
failure log reports what they deleted. Wrapping the loop in one transaction would
turn a partial cleanup into no cleanup and hold every deleted row's locks to the
end.

The loop stops when a batch returns fewer rows than `batchSize` — the eligible set
is exhausted — or when `SessionCleanupMaxBatches` is reached. Reaching the cap
sets `backlog_capped`, which means **"this run stopped because it hit its limit,
not because there was nothing left"**. It is not a backlog count, and no query is
run to produce one: the number would be a second scan of the same predicate,
reported by nothing and acted on by nothing. The next execution reports its own
result.

The cap is what keeps one request bounded regardless of how large the backlog is.
At a daily cadence, dead sessions accumulate at roughly `total_sessions / 30` per
day from idle expiry alone, so a single fixed batch cannot keep up with a table
that grows — which is why the loop exists rather than one larger statement.

### Scheduling

**The schedule is configured externally and nothing in this process sets it.**
The intended cadence is once per day. There is no in-process timer, no Asynq task
and no advisory lock: an external scheduler calls the route, and the operation is
idempotent and safe to run concurrently with itself because the statement takes
`FOR UPDATE SKIP LOCKED`.

### Cost, and the index the cascade needed

`refresh_tokens.replaced_by` had no index. Deleting a session cascades into
`refresh_tokens`, and each deleted token row fires `replaced_by`'s `ON DELETE SET
NULL` action — which, without an index, is a sequential scan of `refresh_tokens`
per deleted row. The cost therefore scales as *tokens deleted × size of
`refresh_tokens`*, and it grows exactly when the table it cleans up has grown.

Measured on a disposable PostgreSQL 18 container running this project's baseline
schema, same statement, warm cache, all buffers shared hits, deleting 100
sessions (500 token rows) against a 250,000-row `refresh_tokens`:

| Schema                                     | Execution time |
| ------------------------------------------ | -------------- |
| as migration 000013 left it                | 8,205 ms       |
| with `refresh_tokens_replaced_by_idx`       | 4.5 ms         |

Migration `000028_add_refresh_tokens_replaced_by_index` adds it. The same cascade
is reachable today through `sessions.user_id → users(id) ON DELETE CASCADE`, so
the hazard was latent before cleanup shipped rather than created by it.

**No index was added to `sessions`.** The three conditions are an `OR`, and an
`OR` cannot be served from a single-column index: with indexes on both
`last_activity_at` and `absolute_expires_at` present, the planner used neither and
walked `sessions_pkey` applying the filter. That walk is also the cheaper plan
here — `ORDER BY id LIMIT n` has to return rows in creation order anyway, and the
oldest sessions are the ones most likely to be expired. At 550,000 sessions with
6% eligible, finding 500 candidates took 2.8 ms.

**The accepted limitation is that this statement may scan many session rows.** It
reads forward through `sessions_pkey` until it has `LIMIT` matches, so on a table
where eligible sessions are a small fraction it examines far more rows than it
deletes. That is the cost of not indexing, it is not currently a problem, and the
measurement above is a disposable container on synthetic data rather than a
production figure.

**What would justify revisiting it.** An index on `sessions` should be added only
on evidence from a real deployment, specifically:

- an `EXPLAIN (ANALYZE, BUFFERS)` of the production statement showing
  `Rows Removed by Filter` high relative to `actual rows`, meaning the walk is
  discarding most of what it reads;
- or `sessions` grown past the point where the selection cost measured above
  becomes material against the request budget;
- or `pg_stat_user_tables` showing the scan reading most of the relation on each
  run.

The index to reach for is then on `last_activity_at` or `absolute_expires_at` —
**not** on `revoked_at`, which is the least selective branch of the three. And
none of it would help without also deciding what to do about the `OR`: a
single-column index does not serve an `OR` of three conditions, so an index
added on that evidence alone would have to be a partial or expression index whose
predicate covers all three, or the query rewritten into a union. That is a
schema and query change, and it should be made against a measured plan rather
than in anticipation of one.

### Authorization

The route is guarded by a shared secret in `X-Cleanup-Secret`, configured with
the optional `SESSION_CLEANUP_SECRET` environment variable.

| Property                     | Decision                                                                                          |
| ---------------------------- | ------------------------------------------------------------------------------------------------- |
| Missing secret in config     | Every request is refused. Startup is unaffected.                                                  |
| Missing, empty, wrong secret | Identical `401 CLEANUP_SECRET_INVALID` with an identical message.                                 |
| Comparison                   | `subtle.ConstantTimeCompare`, as `internal/security/password.go` does.                              |
| Rate limiting                | None, deliberately. The budgets fail closed on Redis, which is wrong for an operator wanting to run cleanup while something else is broken. |
| Batch size from the caller   | Not exposed. A holder of the secret cannot choose how much one request destroys.                   |

The secret is **optional configuration**. `config.Load` requires
`DATABASE_URL`, `VERIFICATION_CODE_SECRET`, `ACCESS_TOKEN_SECRET`,
`GOOGLE_CLIENT_ID` and `REDIS_URL` because nothing works without them; this one
guards a maintenance operation the application can plainly live without, and
requiring it would stop the API from starting over a scheduler nobody has
configured yet. An unset secret leaves every other route untouched.

The refusal carries one code and one message for every failure, and the underlying
error is `nil`. `httpx.NewErrorHandler` logs `zap.Error(err)`, so wrapping the
compared value in an error would write the secret into the log stream. Nothing
else in this process logs request headers. The startup line records
`session_cleanup_enabled` as a boolean and never the value.

The guard is a private function in `internal/api/auth/session/routes.go` rather
than a new middleware. It has exactly one caller, and none of the concepts
`internal/middleware` already models — a Bearer token, a rate-limit budget, a
resolved client address — apply to a route that holds no user credential at all.

### What the response does and does not say

```json
{
  "data": { "deleted_sessions": 42 },
  "message": "session cleanup completed"
}
```

Only the count. Batches, batch size, duration and `backlog_capped` are logged and
not returned: they are execution detail for an operator reading a log, and a
scheduler that wanted to reason about them from the response would be reasoning
about how this process batches its work rather than about whether it ran.

**One behavioural change worth stating.** A revoked session that has already been
reclaimed answers `REFRESH_TOKEN_INVALID` rather than `SESSION_REVOKED`, because
its token row no longer exists to look up. Both are `401`, and both mean the same
thing to a client, but the code differs.

## Security invariants

### Enforced by the schema

| Invariant                              | Mechanism                                                            |
| -------------------------------------- | -------------------------------------------------------------------- |
| A token hash names at most one row     | `refresh_tokens_token_hash_idx` UNIQUE                               |
| A token belongs to an existing session | FK `session_id` → `sessions(id)` ON DELETE CASCADE                   |
| `replaced_by` names a real token row   | FK → `refresh_tokens(id)` **ON DELETE SET NULL** — see "Maintenance" |
| A session belongs to a real user       | FK `user_id` → `users(id)` ON DELETE CASCADE                         |
| Deleting a user removes their sessions | That CASCADE                                                         |

### Enforced by the transaction and the lock

`GetRefreshTokenWithSessionForUpdate` is the only read of a refresh token
anywhere in the codebase:

```sql
FROM refresh_tokens rt
JOIN sessions s ON s.id = rt.session_id
WHERE rt.token_hash = $1
FOR UPDATE OF rt, s
```

Locking **both** rows, for the whole decision, is what makes the following true:

- **Two requests cannot both rotate one token.** The second blocks on the token
  row. When the first commits, PostgreSQL re-evaluates the query against the new
  row version; `token_hash` is unchanged so the row still matches, and the
  second request reads it _with `replaced_by` already set_ — which is the reuse
  branch. Without the lock this would be two rotations from one token.
- **The reuse check and the rotation cannot interleave.** Reuse detection is
  itself a refresh on the same row, so it queues behind the rotation rather than
  observing a half-finished one.
- **A successor cannot outlive a concurrent revocation.** Because the session row
  is locked for the same duration as the decision, a rotation that commits first
  is visible to a revocation that follows, and a revocation that commits first is
  visible to the rotation. Whichever order, the session ends revoked.
- **The revocation predicate is satisfied by construction.** In the reuse and
  expiry branches, the `user_id` and `session_id` passed to `RevokeSession` are
  read from the locked row, not chosen by the caller. A refresh can only ever
  revoke the session its own token belongs to.

There is deliberately **no unlocked read** of a refresh token together with its
session. A read without the lock cannot answer "is this token usable" safely,
because rotation may commit between the read and whatever the caller does with the
answer, so exposing one would invite an authorization decision made outside the
lock.

### Enforced by application convention, not by the database

- **At most one unreplaced token per session.** Rotation preserves the count
  (one consumed, one created) and the session-row lock serializes any pair of
  rotations, so the invariant holds inductively — but no partial unique index
  enforces it. It is a property of the code, not of the schema.
- **Reuse is detectable at all.** This depends entirely on `replaced_by`
  surviving; see "Maintenance".

### Ownership

Every read and write that has a user in scope is scoped by `id AND user_id`:
`RevokeSession` and `RevokeSessionForUser` both carry the predicate, and
`ListSessions` / `CountSessions` filter on it. A UUID is an identifier, never an
authorization decision, and the session id in `DELETE` is the one place a caller
gets to choose one — so that query's `AND user_id = $2` is load-bearing.

## Concurrency

| Race                                              | What the current SQL guarantees                                                                                                                                       |
| ------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Two refreshes, same token                         | Exactly one rotation. The loser re-reads the updated row, sees `replaced_by` set, revokes the session.                                                                |
| Two refreshes, two distinct tokens of one session | Both lock distinct token rows and serialize on the session row. Neither holds what the other needs first, so there is no deadlock cycle; the second rotates normally. |
| Refresh vs logout / `DELETE`                      | Serialized on the session row. If the refresh commits first, the successor belongs to a now-revoked session and the refresh path refuses it on the next use.          |
| Refresh vs absolute expiry                        | Not a race. `absolute_expires_at` is immutable, so the check runs against a stable value; whichever transaction wins the lock decides, and both outcomes are safe.    |
| Reuse detection vs another refresh                | Both are refreshes on the same row, so they queue. Neither can observe a partially rotated token.                                                                     |
| Listing vs session creation or revocation         | `ListSessions` takes no locks, so a page can include a session revoked microseconds later. Expected for a device list; each statement sees its own snapshot.          |
| Session creation vs the user being deleted        | FK CASCADE; not a source of orphans.                                                                                                                                  |

Deadlock sweep: `RefreshToken` is the only writer touching both tables in one
transaction, and it takes both locks in a single statement, so its order is
fixed. `CreateSession` inserts parent then child (the FK edge only). The
password-reset revoke-all touches `sessions` alone. No cycle exists.

**Not guaranteed:** `ListSessions` and `CountSessions` read committed data with no
shared snapshot; the two can disagree within one response.

## Rate limiting refresh

`POST /auth/refresh` is the one session route that carries a budget: **60 requests
per 10 minutes per resolved client IP**, under the `refresh-ip` namespace. It is
`middleware.IPRateLimit`, mounted on the route in front of body binding and in
front of the handler, using the same `c.IP()` and `CLIENT_IP_SOURCE` /
`TRUSTED_PROXIES` configuration as every other budget in the application. There is
no second place here where a forwarding header is read.

**Why this route and not the others.** The other three are authenticated and cost
one or two indexed statements, which is the same shape as the authenticated reads
`AGENTS.md` already leaves unlimited — "a limit here would bound no shared
resource." Refresh is different because of what rotation does to an attacker.

**The threat is rotation chaining.** A caller holding one stolen refresh token can
present it, receive its successor, and present that. Always using the newest token,
they never present one with `replaced_by` set, so the reuse detection described
above never fires against them. Each such request is indistinguishable from a
legitimate one at the service layer, and each writes a `refresh_tokens` row that
nothing ever removes. The chain is bounded only by `absolute_expires_at` — ninety
days — which is a long time to be unbounded by rate.

This is also why the subject is the client address and **not** the refresh token: a
token-keyed budget would be defeated by exactly the chaining it is meant to bound,
since every successor is a new hash and would land on a counter of its own. A
user-keyed budget is not available either — the user id is only known from the
locked row read inside the refresh transaction, so a limiter placed before the
work cannot key on it and one placed after it bounds nothing that has not already
happened.

**The threshold is derived, and it is still an estimate.** A client that refreshes
only when it must does so once per `AccessTokenLifetime`, i.e. 0.67 times in ten
minutes, so sixty in ten leaves room for roughly ninety devices on one address
refreshing continuously with no burst at all, and about thirty of them through a
threefold burst. Refresh is the highest-frequency legitimate route in the
application — every active device does it automatically — which is why it sits at
the same value as `password-reset-status-ip` rather than below it.

**These are initial engineering estimates, not production-validated thresholds.**
No deployment has been selected and no traffic has been observed. The assumption
this rests on is the number of devices behind a shared address, which is not
knowable from here. The assumption most likely to require raising it is client
retry behaviour: a client that retries refresh aggressively multiplies its own
baseline, and that client lives outside this repository. Reassess both against real
traffic before relying on the value.

**`refresh-ip` is its own counter.** It is never `login-ip` and never
`google-auth-ip`, for the same reason every namespace in the application is
distinct: an exhausted refresh budget must never become a way to lock a user out of
signing in with a password or a Google token they still hold. `AGENTS.md` requires
the same separation for the login budgets, and
`TestRefreshIPRateLimit_DoesNotSpendLoginOrGoogleBudgets` pins it in both
directions.

**Charging before body binding is deliberate.** A body that cannot be parsed is
still counted, because the cheapest request a caller can send is one that cannot
succeed. A charge behind the binding would count only the requests that were
already well-formed.

### The availability trade-off

The limiter **fails closed**, like every other budget in the application. That is
the correct default and it is not changed here; the consequence is worth stating
plainly rather than discovering during an outage.

When Redis is unreachable:

- `/auth/refresh` answers `503 IP_RATE_LIMIT_UNAVAILABLE`. The handler never
  runs, so no rotation happens and no token is consumed. No `Retry-After` is
  sent, because there is no window to name.
- **Users with a still-valid access token are unaffected.** Access tokens are
  stateless and `middleware.Auth` never consults Redis, so an access token keeps
  working for its remaining lifetime — up to 15 minutes from the moment of the
  outage.
- Once that access token expires, the client cannot renew and the user is locked
  out until Redis recovers. Recovery is automatic and lossless: the refresh token
  was never touched.
- `POST /auth/logout` carries no budget, so a user is never locked into a session.

Before this change, a Redis outage stopped login, registration and password reset
but left refresh working — the last remaining door. This budget closes that door
too, so a Redis outage now blocks renewal as well as new sign-ins. That is a
deliberate trade: a bounded ceiling on the work one stolen credential can cause,
in exchange for coupling the recovery path to the same Redis every other budget
already needs.

## Accepted trade-offs

These are decisions, not open questions. Changing one is a policy change.

- **A concurrent double-refresh logs the user out.** One request rotates, the
  other is read as a replay and revokes the session. The implementation cannot
  tell a client retry from an attacker's replay, and the alternative — tolerating
  both — leaves reuse detection unenforceable. The cost is that a legitimate
  client firing two refreshes at once must sign in again.
- **Access tokens outlive revocation by up to 15 minutes.** Stateless by design;
  the window is `AccessTokenLifetime`.
- **A lost refresh response costs the session.** If the server commits a rotation
  and the response never arrives, the client's retry presents the old token and
  trips reuse detection. This is the same policy as the first item, seen from the
  network instead of from two goroutines.
- **`DELETE /auth/sessions/:id` is not idempotent.** A second call answers 404
  where logout answers 200. A client retrying a revoke after a lost response sees
  a 404 and may believe the session is still live.
- **No per-user session cap.** Every successful authentication creates a session
  and nothing counts them. The ceiling on the Google path is the per-IP budget,
  which bounds the rate and not the total.
- **Cleanup is externally scheduled and best-effort.** Dead sessions are removed
  by `POST /internal/maintenance/sessions/cleanup`, which nothing in this process
  calls. A deployment that never configures a scheduler never cleans up, and the
  tables keep growing exactly as they did before; the feature is additive, not a
  replacement for a job that runs. It is also bounded per request: a run stops
  after 100 batches of 500 and reports `backlog_capped`, so a backlog larger than
  one request's reach needs more than one execution to clear.

## Open items

Neither of these is a decision. Both are recorded rather than acted on.

- **`POST /auth/refresh` has no rate-limit budget.** The auth module's working
  rule is that a route reachable without a credential carries a per-IP budget
  unless it is a pure read that consumes nothing: manual login has `login-ip`, the
  three Google routes share `google-auth-ip`, PIN issuance and PIN verification
  each have their own, and the two continuation reads are deliberately unlimited
  for exactly that reason — the routes say so in their own comments. Refresh is
  neither bounded nor a pure read. Every request opens a transaction, and a
  request carrying a valid token inserts a refresh-token row and takes an
  exclusive lock on a session. A budget would need its own namespace, since
  `AGENTS.md` forbids sharing a counter across flows and the limiter fails
  closed, so a shared budget would let one exhausted counter take refresh offline
  alongside the login routes.
- **The idle-window check mixes two clocks.** `last_activity_at` is written by
  PostgreSQL `NOW()` and compared against Go's `time.Now()`. `absolute_expires_at`
  is written and compared in Go and is internally consistent. Ordinary clock drift
  is absorbed by a 30-day window, but evaluating both in SQL would make the
  database the single authority.

  This remains open **for the runtime refresh path**, which is unchanged. The
  cleanup statement does not share it: cleanup evaluates all three of its
  conditions against `NOW()`, so it reads the database clock consistently. Closing
  the item for refresh would mean moving that comparison into SQL, which is a
  change to the refresh decision and not part of this work.

## Testing

### Unit tests (mocked repository)

`internal/api/auth/session/*_service_test.go`. These prove what the **service**
does — which arguments reach the repository, and which errors come back:

- `create_session_service_test.go` — the hash handed to the repository is
  `HashToken` of the token returned to the client, so the raw value is provably
  not persisted; `absolute_expires_at` is bracketed between two `time.Now()`
  samples rather than compared to a constant.
- `refresh_token_service_test.go` — both hashes and the idle policy are forwarded,
  and the issued access token is re-parsed to confirm it carries the `sub`/`sid`
  the **repository returned** rather than anything the caller supplied.
- `logout_service_test.go`, `revoke_session_for_user_service_test.go` — both
  identifiers are forwarded, and "matched nothing" is a success for logout.
- `list_sessions_service_test.go` — page and limit clamping, and both repository
  error paths.
- `cleanup_sessions_service_test.go` — the batch loop: stopping on a short batch,
  stopping at `SessionCleanupMaxBatches` with `backlog_capped`, and the values the
  repository is handed (`SessionIdleLifetime` and the fixed batch size, so neither
  is caller-controlled). It is also the only place the **logs** can be asserted,
  because a failed run returns no result: the committed deletion count, the
  completed batch count and the zero-deletion success line exist only there, and
  `zaptest/observer` reads them without adding a dependency.

### Integration tests (real PostgreSQL)

These prove what the **database** does. Five files, all against the shared
testcontainer:

- `internal/integration/auth_session_test.go` — creation; **rollback when the
  refresh-token insert fails**; rotation and successor linkage; invalid, revoked
  and already-rotated tokens; absolute and idle expiry (each asserting the
  session really ended revoked); logout ownership, idempotency and silence.
- `internal/integration/auth_session_concurrency_test.go` — two real races, each
  asserting the **persisted** state afterwards rather than the response code.
- `internal/integration/auth_session_revoke_api_test.go` — `DELETE` ownership
  across two users, the owner's own success, and that an unknown id produces the
  identical non-enumerating refusal.
- `internal/integration/auth_session_list_test.go` — listing and counting are
  owner-scoped and exclude revoked rows, ordering, and that paging partitions the
  active set with no duplicates or omissions.
- `internal/integration/auth_session_cleanup_test.go` — an active session and its
  tokens survive; revoked, absolute-expired and idle-expired sessions are each
  removed; a session inside the idle window is kept while one outside it is not;
  **the cascade removes the whole `replaced_by` chain**; every unusable secret —
  absent, empty, wrong, wrong case, a prefix, and a deployment that configured
  none — is answered identically and mutates nothing; an empty run succeeds; and
  repeated execution is safe. The batch boundary is crossed by seeding
  `SessionCleanupBatchSize + 1` dead rows, since a total above one batch cannot be
  produced by one statement.

### Why these cannot be mocked

A mock repository proves the service called the right method with the right
arguments. It cannot answer any of the questions this feature actually turns on:

- Which of two concurrent transactions wins a row lock.
- Whether a second writer re-reads the _updated_ row version (PostgreSQL's
  EvalPlanQual behaviour) or the one it blocked on.
- Whether a unique index fires and the first insert in the same transaction is
  rolled back with it.
- Whether a predicate that _should_ match nothing actually matches nothing.

**The Go race detector does not substitute for this.** `-race` instruments Go
memory access. Two goroutines contending on a PostgreSQL row share no memory —
the database serializes them — so the detector sees nothing and reports nothing.
A green `-race` run says nothing about whether the loser of a refresh correctly
observed the winner's commit. Conversely the integration tests exercise real
threads and the Go-side harness (`runConcurrently`, gomock controllers), so
`-race` is still worth running over the package; the two cover different halves
of the same behaviour.

Repeated runs (`-count`) exercise deadlock and liveness, not data races, and are
not a substitute for the detector either.

## Maintenance

### Pruning refresh tokens would silently disable reuse detection

`refresh_tokens.replaced_by` is declared `ON DELETE SET NULL`. **If a successor
row is ever deleted, its predecessor's `replaced_by` becomes NULL** — which is
indistinguishable from a token that was never rotated. That token would then be
accepted again and rotated a second time, and the reuse signal for that chain
would be gone with no error anywhere.

This is **no longer latent**. `POST /internal/maintenance/sessions/cleanup` is the
retention job this section warned about, and it complies with the rule below by
deleting only from `sessions` and letting the existing cascade take each chain
whole.

If a retention policy is ever changed, it must continue to remove whole sessions
or nothing, and must not delete an individual token row that another row's
`replaced_by` still points at.

The index this cascade needs is `refresh_tokens_replaced_by_idx`, added by
migration `000028_add_refresh_tokens_replaced_by_index`. It is a pure lookup
structure: `ON DELETE SET NULL` fires whether or not an index exists, so the
`replaced_by` NULLing described above is unchanged by its presence, and nothing
about reuse detection depends on it. It exists because the `SET NULL` action
otherwise scans `refresh_tokens` once per deleted token row — see "Session
cleanup" above for the measurement.

### Where the code is

| Concern                           | Location                                                                          |
| --------------------------------- | --------------------------------------------------------------------------------- |
| Routes and the cleanup guard      | `internal/api/auth/session/routes.go`                                             |
| Handlers                          | `internal/api/auth/session/handler.go`                                            |
| Service                           | `internal/api/auth/session/service.go`                                            |
| Transactions and SQL calls        | `internal/api/auth/session/repository.go`                                         |
| Queries                           | `internal/api/auth/session/queries.sql`                                           |
| Constants and error codes         | `internal/api/auth/session/constants.go`, `error_codes.go`                        |
| Request/response shapes           | `internal/api/auth/session/request.go`, `response.go`                             |
| Client metadata extraction        | `internal/api/auth/session/metadata.go`                                           |
| Access token                      | `internal/security/access_token.go`                                               |
| Token generation and hash         | `internal/security/token.go`                                                      |
| Bearer guard                      | `internal/middleware/auth.go`                                                     |
| Cleanup secret configuration      | `internal/config/config.go` (`SESSION_CLEANUP_SECRET`)                            |
| Wiring                            | `internal/api/auth/module.go`                                                     |
| Schema                            | `migrations/000013_create_auth_sessions_and_refresh_tokens.up.sql`                |
| Cascade index                     | `migrations/000028_add_refresh_tokens_replaced_by_index.up.sql`                   |
| Session creation callers          | `internal/api/auth/login/service.go`, `internal/api/auth/registration/service.go` |
| Bulk revocation on password reset | `internal/api/auth/password_reset/repository.go` (`RevokeAllSessionsForUser`)     |
| Test-only SQL                     | `internal/testutil/db/session/queries.sql`                                        |

A password reset revokes every session the user has, in the same transaction that
writes the new credential. It writes `sessions.revoked_at` only and leaves the
token rows alone, which is sufficient: the refresh path re-reads
`sessions.revoked_at` under its lock, so a token that survives the reset cannot
be used against the sessions it belonged to.

## Session listing ordering

`ListSessions` orders by `last_activity_at DESC, id DESC`:

```sql
ORDER BY last_activity_at DESC, id DESC
LIMIT $3 OFFSET $2
```

The `id DESC` tie-breaker is load-bearing, because `last_activity_at` is **not
unique**. It defaults to `NOW()`, which is constant within a transaction, and
`UpdateSessionActivity` writes `NOW()` as well, so sessions created or refreshed
together can carry the same value. Without a second sort key a tied group is free
to come back in any order, which under `LIMIT`/`OFFSET` is how a client paging
through a device list can be shown one session twice and another not at all while
the dataset never changed and the total never moved.

`sessions.id` is `uuidv7()` with no overriding trigger, so it is unique and
monotonic in creation order. The tie-breaker is therefore both total and the right
way round: among sessions that tie, the most recently created is listed first.
This is the same rule `AGENTS.md` states for the collection listings.

**What this does not fix.** The sort key is still a **moving** one. Refreshing on
any device rewrites `last_activity_at`, so rows can reorder between two page
requests even when no timestamps tie and even though the ordering is now total.
That is inherent to offset pagination over mutable data, not to the tie-breaker,
and closing it would need either an immutable sort key or keyset pagination.

Converting this endpoint from `page`/`limit` to a cursor is a separate product
decision already recorded for the collection listings; `PROGRESS.md` decision 19f
keeps this endpoint on offset pagination.
