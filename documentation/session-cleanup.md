# Session cleanup

Purpose of this document: record how dead sessions are removed, which sessions are
eligible and why that can never shorten a live one, how a run is bounded, what it
costs, and how the route is guarded. It is the reference for the cleanup feature.
`auth-sessions.md` is the reference for the sessions being cleaned up, and its
"Maintenance" section states the rule this feature has to keep obeying.

## What it removes

A session refreshed every fifteen minutes for its full ninety-day lifetime
accumulates roughly 8,600 `refresh_tokens` rows, and without a retention job the
`sessions` table only ever grows. `POST /internal/maintenance/sessions/cleanup`
removes dead sessions.

## The predicate

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
recorded in `auth-sessions.md` under "Open items". The two only disagree if the
API host and the database clocks diverge by more than thirty days, which is not a
state that occurs.

## Only the parent row is deleted

The statement removes rows from `sessions` and nothing else.
`refresh_tokens.session_id → sessions(id) ON DELETE CASCADE` takes each session's
whole token chain with it in the same statement.

**No statement anywhere deletes an individual refresh token while its session
survives.** That is the constraint recorded in `auth-sessions.md` under
"Maintenance" ("Pruning refresh tokens would silently disable reuse detection"),
and it is the reason cleanup is built this way. Deleting the parent deletes the
chain atomically and cannot leave a predecessor pointing at nothing; deleting one
token out of a chain would.

## Batching

One execution runs the bounded statement repeatedly:

| Constant                   | Value | What it bounds                            |
| -------------------------- | ----- | ----------------------------------------- |
| `SessionCleanupBatchSize`  | 500   | rows locked and cascaded by one statement |
| `SessionCleanupMaxBatches` | 100   | how many statements one request may run   |

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

## Scheduling

**The schedule is configured externally and nothing in this process sets it.**
The intended cadence is once per day. There is no in-process timer, no Asynq task
and no advisory lock: an external scheduler calls the route, and the operation is
idempotent and safe to run concurrently with itself because the statement takes
`FOR UPDATE SKIP LOCKED`.

## Cost, and the index the cascade needs

`refresh_tokens.replaced_by` needs an index. Deleting a session cascades into
`refresh_tokens`, and each deleted token row fires `replaced_by`'s `ON DELETE SET
NULL` action — which, without one, is a sequential scan of `refresh_tokens` per
deleted row. The cost therefore scales as *tokens deleted × size of
`refresh_tokens`*, and it grows exactly when the table it cleans up has grown.

Measured on a disposable PostgreSQL 18 container running this project's baseline
schema, same statement, warm cache, all buffers shared hits, deleting 100
sessions (500 token rows) against a 250,000-row `refresh_tokens`:

| Schema                                         | Execution time |
| ---------------------------------------------- | -------------- |
| without the index (schema of migration 000013) | 8,205 ms       |
| with `refresh_tokens_replaced_by_idx`          | 4.5 ms         |

Migration `000028_add_refresh_tokens_replaced_by_index` adds it. The same cascade
is reachable through `sessions.user_id → users(id) ON DELETE CASCADE`, so the
hazard exists independently of cleanup.

**`sessions` has no index for this statement.** The three conditions are an `OR`, and an
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

## Authorization

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

## Route placement

The cleanup route is deliberately **not** mounted under `/auth`. Every `/auth`
route either carries a Bearer token or takes its credential in the body, and an
operator-triggered maintenance call holds neither; putting it in that namespace
would stop the prefix meaning what it means. `session.MaintenanceRoutes` registers
it on its own group, which is the arrangement `collection` already uses for its
two resources.

## What the response does and does not say

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

**Observable effect on refresh.** Once a revoked session has been reclaimed,
presenting its token answers `REFRESH_TOKEN_INVALID` rather than `SESSION_REVOKED`,
because its token row no longer exists to look up. Both are `401` and both mean
the same thing to a client, but the code differs.

## Testing

Service-level tests (`cleanup_sessions_service_test.go`) cover the batch loop and
the arguments the repository receives: `SessionIdleLifetime` and the fixed batch
size, so neither is caller-controlled. They are also the only place the **logs**
can be asserted. A failed run returns no result, so the committed deletion count,
the completed batch count and the zero-deletion success line exist only in the log;
`zaptest/observer` reads them without adding a dependency.

Integration tests (`auth_session_cleanup_test.go`) prove what the database does:

- Active sessions and their tokens survive. Revoked, absolute-expired and
  idle-expired sessions are each removed, and a session inside the idle window is
  kept while one outside it is not.
- **The cascade removes the whole `replaced_by` chain.**
- Every unusable secret — absent, empty, wrong, wrong case, a prefix, and a
  deployment that configured none — is answered identically and mutates nothing.
- The batch boundary is crossed by seeding `SessionCleanupBatchSize + 1` dead rows,
  since a total above one batch cannot be produced by one statement. An empty run
  succeeds and repeated execution is safe.

## Where the code is

| Concern                  | Location                                                        |
| ------------------------ | --------------------------------------------------------------- |
| Route and secret guard   | `internal/api/auth/session/routes.go` (`MaintenanceRoutes`)     |
| Secret configuration     | `internal/config/config.go` (`SESSION_CLEANUP_SECRET`)          |
| Cascade index            | `migrations/000028_add_refresh_tokens_replaced_by_index.up.sql` |
| Service tests            | `internal/api/auth/session/cleanup_sessions_service_test.go`    |
| Integration tests        | `internal/integration/auth_session_cleanup_test.go`             |
