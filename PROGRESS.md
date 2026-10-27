# PROGRESS.md

Handoff notes for the next coding session. Read `AGENTS.md` first, then
`documentation/product.md`, then this file.

This is a handoff, not a diary. Keep it accurate and short.

## Latest change

### 2026-10-27 — Manual registration recovery eligibility tightened

`POST /auth/register/recovery/manual` now starts recovery only for a manual
registration that has passed every eligibility check. It previously returned
`201` with the registration's normal verification id while that verification was
still `pending`, which reported a recovery that had not happened and handed the
caller an id belonging to the normal flow.

The registration, its type, its deadline and its normal verification status are
re-read and re-checked inside `InitiateManualRecovery` from the row locked by
`GetPendingRegistrationByEmailForUpdate`, so all of them are the database state
at the moment of the decision rather than the request or a value read earlier.
The completed-registration pre-check runs before the transaction and only refines
the refusal; the locked lookup is what actually excludes a completed, expired or
replaced registration.

| Condition                                                  | Result                                              |
| ---------------------------------------------------------- | --------------------------------------------------- |
| no completed registration and no pending row               | **409** `REGISTRATION_NOT_PENDING`                  |
| a completed registration for the address                   | **409** `REGISTRATION_ALREADY_COMPLETED`            |
| registration is not manual                                 | **409** `INVALID_REGISTRATION_TYPE`                 |
| registration has expired                                   | **409** `REGISTRATION_EXPIRED`                      |
| normal verification has not reached `verified`             | **409** `REGISTRATION_ALREADY_PENDING`              |
| all of the above pass                                      | **201**, reuse the open recovery request or create one |

- **`REGISTRATION_ALREADY_PENDING` is an existing constant, not a new code.** It
  was declared but never emitted; this endpoint is its first user. It is the
  accurate code for the condition — the registration *is* pending, which is why
  recovery cannot start — where `REGISTRATION_NOT_PENDING` would say the
  opposite. No other error mapping changed.
- **`verification_requests_status_check` permits only `pending` and `verified`,
  so "not verified" has exactly one reachable value.** The guard is still
  written as `!= verified`, so a future status cannot silently become eligible.
  An integration test asserts the schema domain rather than pretending a third
  fixture exists.
- **Nothing is created or reused before every check passes.** The open-recovery
  lookup runs after the checks, so an ineligible registration cannot have its
  still-open recovery request handed back.
- **No state is touched on refusal.** Initiation still does not expire or replace
  the registration and does not invalidate its continuation; replacement happens
  only in `CompleteRecoveryVerification` after the recovery PIN verifies.
- **Concurrency is unchanged.** The registration row lock still serializes
  initiations, normal registration and finalization; the eligibility re-checks
  run under that lock; concurrent initiations still converge on one open
  recovery request.

Contract impact: the recovery endpoint now answers **409
`REGISTRATION_ALREADY_PENDING`** for a state that used to be `201`. A client
receiving it should continue through `POST /auth/register/manual`, which returns
the same normal verification id with `verification_status: pending`; the
recovery endpoint no longer returns a verification id for that case. The Flutter
client must map the code (or treat this 409 as "use the normal registration
flow"). No new error code was introduced.

Files changed:

- `internal/api/auth/registration/repository.go` — pending verification is now a
  refusal; doc comment states the verified precondition.
- `internal/api/auth/registration/service.go` — doc comment records the
  eligibility rule and the refusal code.
- `internal/api/auth/registration/error_codes.go` — comment for the first real
  use of `REGISTRATION_ALREADY_PENDING`.
- `internal/api/auth/registration/handler.go` — Swagger description and error
  list.
- `docs/docs.go`, `docs/swagger.json`, `docs/swagger.yaml` — regenerated; the
  recovery description updated.
- `internal/api/auth/registration/recovery_service_test.go` — service-level
  pass-through of the 409 refusal, asserting no verification id is returned.
- `internal/integration/auth_register_recovery_test.go` — pending verification
  rejected, expired registration rejected, non-pending registration rejected,
  rejected initiation preserves registration/continuation/verification state,
  open recovery reused only when eligible, completed rejection now asserts no
  recovery request, and the verification-status domain assertion.
- `PROGRESS.md` — this entry.

No schema change and no new migration. Migration **000029**
(`add_registration_recovery_purpose`) belongs to the uncommitted recovery work
this tightens; it was not created or applied by this change. No `sqlc generate`
was needed because no query changed.

Verification (2026-10-27, Windows, Docker running; race runs used the
MSYS2/MinGW toolchain with `CGO_ENABLED=1` and
`CC=C:\msys64\ucrt64\bin\gcc.exe`):

```sh
gofmt -w <changed .go files>                      # clean
swag init -g cmd/api/main.go -parseInternal       # exit 0; recovery description updated
go build ./...                                    # exit 0
go vet ./...                                      # exit 0
go test ./internal/api/auth/registration/... -count=1
                                                  # exit 0
go test ./internal/integration -run 'TestRegisterManualRecovery_' -count=1 -v -timeout 20m
                                                  # exit 0, all cases
go test ./internal/integration -run 'TestRegisterManual_|TestCreateSocialRegistration_|TestFinalizeSocialRegistration_|TestPasswordReset_|TestLoginGoogle_' -count=1 -timeout 20m
                                                  # exit 0
go test -race ./internal/api/auth/registration/... -count=1 -timeout 20m
                                                  # exit 0
go test -race ./internal/integration -run 'TestRegisterManualRecovery_' -count=1 -timeout 30m
                                                  # exit 0
go test -race ./internal/integration -run 'TestRegisterManualRecovery_Concurrent|TestRegisterManualRecovery_RaceWithFinalization|TestRegisterManualRecovery_NormalRegistrationDoesNotReplaceOpenRecovery|TestRegisterManualRecovery_InitiationRacesWithNormalRegistration' -count=5 -timeout 30m
                                                  # exit 0, no flakes
go test ./... -count=1 -timeout 30m               # exit 0, every package
```

Remaining limitations:

- **The Flutter client is not updated.** Until it maps
  `REGISTRATION_ALREADY_PENDING`, a user calling recovery before verifying the
  normal registration sees a 409 it may not recognize. The corrective call is
  `POST /auth/register/manual`, whose response already carries
  `verification_status: pending`.
- **The "other ineligible verification status" case has no reachable fixture.**
  The schema permits exactly `pending` and `verified`; the `pending` refusal is
  the whole branch, and the guard is `!= verified` so a future value is refused
  by default.
- **A registration with no `purpose = 'registration'` verification row is not
  visible to the locked lookup** and is refused as `REGISTRATION_NOT_PENDING`,
  the same answer an address with no registration receives.
- No migration was executed.

Nothing was committed.

### 2026-10-27 — Manual registration recovery signal

`POST /auth/register/manual` now reports `verification_status` alongside
`verification_id`. The field is additive; the status code, message, error codes
and existing `verification_id` are unchanged.

- `pending` — request a PIN as before. This covers a new registration, a reused
  pending verification, an expired-registration replacement, and the
  manual-supersedes-social path.
- `verified` — PIN verification has already completed and a usable registration
  continuation exists. The client must **not** request or submit another PIN. It
  uses the raw continuation token it stored after `/verify` with
  `GET /auth/register/continuation`, then runs the existing finalization flow.
  The token is never returned by this endpoint; only its hash exists server-side.
- No usable continuation (expired, consumed, or absent) still takes the existing
  recovery: the old registration is expired (not completed) and a replacement
  registration plus pending verification request is created; the response is
  `pending` with a new `verification_id`.

This is the signal the 2026-10-26 entry's manual recovery was missing. That
entry's live-continuation reuse was correct but indistinguishable from a pending
registration, so a client that followed the normal PIN flow still saw
`VERIFICATION_NOT_PENDING`. This entry supersedes its “No API contract change and
no Swagger change” sentence: the response is additively extended and Swagger is
regenerated.

Threading: `createManualRegistrationOnce` populates the existing
`VerificationRequest.Status` on both manual reuse returns;
`RegisterManualResult.VerificationStatus` carries it through the service; the
handler maps it into `RegisterResponse.VerificationStatus`. The A/B/C decision
logic, the `HasUsableRegistrationContinuation` plain read, the partial unique
index handling and the single retry are unchanged.

Social registration is **unchanged**. `RegisterResponse` is manual-only;
`LoginGoogleResponse`/`mapLoginGoogleResponse` are separate, and
`CreateSocialRegistration` still returns only a verification id. Identity
mismatch is still refused first, and the existing social recovery is unchanged.
A regression assertion in `TestLoginGoogle_RegistrationRequired` checks the
Google response does not contain `verification_status`.

Files changed:

- `internal/api/auth/registration/repository.go` — populate
  `VerificationRequest.Status` on manual reuse.
- `internal/api/auth/registration/service.go` — add/map
  `RegisterManualResult.VerificationStatus`.
- `internal/api/auth/registration/handler.go` — map the field; document both
  states in Swagger.
- `internal/api/auth/registration/response.go` — add `verification_status`
  (`pending`/`verified`). Gofmt also normalized the file's pre-existing spaces
  to tabs and added its missing trailing newline.
- `docs/docs.go`, `docs/swagger.json`, `docs/swagger.yaml` — regenerated;
  additive.
- `internal/api/auth/registration/register_manual_service_test.go` — mapping and
  verified-reuse unit coverage.
- `internal/integration/auth_register_test.go` — first-registration,
  pending-reuse and expired-reconciliation response assertions.
- `internal/integration/auth_registration_lifecycle_recovery_test.go` — verified
  reuse, no-token-leak, replacement and concurrency response assertions; new
  concurrent verified-reuse case.
- `internal/integration/auth_login_social_test.go` — social response isolation
  assertion.
- `PROGRESS.md` — this entry.

No migration, baseline change, `sqlc generate`, password-reset change, social
behavior change, rate-limit change or error-code change.

Verification (2026-10-27), all run on Windows with Docker and the MSYS2/MinGW
toolchain (`CGO_ENABLED=1`, `CC=C:\msys64\ucrt64\bin\gcc.exe`):

```sh
swag init -g cmd/api/main.go -parseInternal       # exit 0; additive schema diff
gofmt -w <changed .go files>                      # clean
go build ./...                                    # exit 0
go vet ./...                                      # exit 0
go test ./... -count=1 -timeout 30m               # exit 0, every package
go test ./internal/integration -run 'TestRegisterManual_|TestCreateSocialRegistration_|TestFinalizeSocialRegistration_|TestPasswordReset_|TestLoginGoogle_' -count=1 -v -timeout 20m
                                                  # exit 0, all cases
go test -race ./internal/api/auth/registration ./internal/api/auth/login -count=1 -timeout 20m
                                                  # exit 0
go test -race ./internal/integration -run 'TestRegisterManual_|TestCreateSocialRegistration_|TestFinalizeSocialRegistration_' -count=1 -timeout 30m
                                                  # exit 0
go test -race ./internal/integration -run 'TestRegisterManual_Concurrent|TestCreateSocialRegistration_Concurrent|TestFinalizeSocialRegistration_Concurrent' -count=5 -timeout 30m
                                                  # exit 0, no flakes
```

`swag init` prints a non-fatal warning about `go list` on the repository root
(no Go files there); it still generated `docs.go`, `swagger.json` and
`swagger.yaml`, and the new schema was verified to contain
`registration.RegisterResponse.verification_status` with enum
`pending`/`verified`.

Remaining limitation: a client that sees `verified` but no longer has the raw
continuation token cannot recover immediately. The backend stores only the hash
and cannot reissue it, and an unauthenticated force-restart would let anyone
cancel a live registration. The supported recovery is to retry `RegisterManual`
after the 15-minute continuation lifetime, when it returns `pending` with a new
verification id. Immediate recovery needs a separate proof-of-mailbox-control
design. Client implementation (persisting the token immediately after
verification, branching on `verification_status`) is out of scope for this
change.

Nothing was committed.

### 2026-10-26 — Registration lifecycle recovery after verification

A manual or social registration whose verification request had reached `verified`
while the registration was still `pending` was reused instead of recovered. The
reuse branch consulted only the registration's own status, so it returned a
verification id that every verification route refuses with **409
`VERIFICATION_NOT_PENDING`**. The only remaining capability was the continuation,
which is stored hashed and is never reissued. Manual and social registrations live
seven days, so the address stayed unusable for the rest of that window.

#### Manual registration

Reuse now requires `verification_status = 'pending'`. When the verification is
terminal, `HasUsableRegistrationContinuation` decides:

| Continuation | Behaviour |
| --- | --- |
| unconsumed and unexpired | registration and verification id retained; the continuation keeps working |
| consumed, expired, or absent | old registration expired, replacement registration and verification request created |

The liveness check is a **plain read** (`SELECT EXISTS`), not a locking join.
Finalization consumes the continuation and then completes the registration, so
locking both in the lookup could invert the lock order and deadlock with it. An
uncommitted consumption is invisible to the read, and a committed one means the
parent is no longer pending, which the lookup has already excluded by locking the
registration row.

A live continuation is **not** replaced because `/auth/register/manual` is
unauthenticated and has no rate limiter: replacing it would let anyone who knows
an address cancel a registration another person is about to finalize. A caller
who lost the token waits out the 15-minute continuation lifetime, then
re-registers and receives a fresh flow. Expired-registration reconciliation is
unchanged and still uses `ExpirePendingRegistration`, whose `expires_at <= NOW()`
guard remains.

#### Social registration

The same verification-status check now applies. The identity-mismatch refusal
still runs **first**, so a different Google identity is still refused while the
registration is unexpired. For the same verified Google identity, a terminal
verification request force-expires the old registration and creates a replacement
with a fresh verification request and pending social identity. No
continuation-liveness gate is needed there: the caller is the Google-verified
identity that opened the registration (`email_verified` is enforced by
`login/google_token_verifier.go`), so replacement is the owner restarting, not a
third party cancelling. The existing `ExpirePendingRegistration` path still
handles registrations past their deadline.

#### Password reset — unchanged, and now pinned

Password reset is **deliberately not changed**. Its reset and its continuation
expire together at fifteen minutes, so the only window in which the request route
returns a terminal verification id is a window with a live continuation.
Replacing it there would let an unauthenticated caller cancel the owner's reset,
and once the reset expires the existing `ExpirePendingPasswordReset` path already
recovers. Returning an error instead would break the non-enumerating `201 + id`
response.

`TestPasswordReset_VerifiedWithLiveContinuationIsReusedAndRemainsUsable` protects
that decision against a future blanket application of the registration fix. It
verifies a reset, requests the reset again, asserts the **same** verification id
and a single pending reset/verification pair, then asserts the original
continuation still validates and still finalizes.

#### Files changed

- `internal/api/auth/registration/queries.sql` — `GetPendingRegistrationByEmailForUpdate`
  now selects `vr.status AS verification_status`; new
  `HasUsableRegistrationContinuation`.
- `internal/api/auth/registration/generated/queries.sql.go` — regenerated with
  `sqlc generate`; not hand-edited.
- `internal/api/auth/registration/repository.go` — `createManualRegistrationOnce`
  and `createSocialRegistrationOnce`.
- `internal/integration/auth_registration_lifecycle_recovery_test.go` — new manual
  and social lifecycle/recovery cases.
- `internal/integration/auth_password_reset_lifecycle_reuse_test.go` — the
  password-reset no-change regression.

No migration, no baseline change, no API contract change and no Swagger change.

#### Verification

All commands below were executed on Windows with Docker and the MSYS2/MinGW
toolchain (`CGO_ENABLED=1`, `CC=C:\msys64\ucrt64\bin\gcc.exe`).

```sh
sqlc generate                                     # exit 0
gofmt -w <changed .go files>                      # clean
go build ./...                                    # exit 0
go vet ./...                                      # exit 0
go test ./... -count=1 -timeout 30m               # exit 0, every package
go test ./internal/integration -run 'TestRegisterManual_|TestCreateSocialRegistration_|TestFinalizeSocialRegistration_|TestPasswordReset_' -count=1 -v -timeout 20m
                                                  # exit 0
go test -race ./internal/api/auth/registration ./internal/api/auth/password_reset ./internal/api/auth/login -count=1 -timeout 20m
                                                  # exit 0
go test -race ./internal/integration -run 'TestRegisterManual_|TestCreateSocialRegistration_|TestFinalizeSocialRegistration_|TestPasswordReset_' -count=1 -timeout 30m
                                                  # exit 0
go test -race ./internal/integration -run '<the five new concurrency cases>' -count=5 -timeout 30m
                                                  # exit 0, no flakes
```

The new integration cases assert persisted state, not just status codes: the
continuation liveness gate (a live continuation survives a re-register and still
finalizes), the expired-continuation replacement, the old verification id being
unable to issue a PIN after replacement, concurrent replacement converging on one
new verification id, and concurrent finalization/replacement leaving no orphaned
user, credential, collection, identity or session.

#### Remaining limitation

An immediate retry after abandoning a **manual** flow with a live continuation
still receives the verified verification id and the client sees
`VERIFICATION_NOT_PENDING` until the 15-minute continuation expires. That is the
deliberate cost of not letting an unauthenticated request cancel a live
capability. Password reset keeps its existing bounded window for the same reason.
The Flutter client should treat that code on a just-returned registration id as
"already verified; use the stored continuation or retry shortly."

Nothing was committed.

### 2026-10-23 — Explicit collection intent and `COLLECTION_NAME_ALREADY_EXISTS`

`PUT /saved-items/:id/collection` now requires `collection_intent`, with values
`existing` and `create`. The name alone could not express the difference: a tapped
row and a typed name produced the same body, so the server could not tell reuse from
"create this".

- `existing` is the old behaviour: any owned collection of either type is reused, and
  if the name no longer resolves, a user collection is still created. It never
  returns `COLLECTION_NAME_ALREADY_EXISTS`.
- `create` asks for a new collection. A name already held by one of the user's
  collections (case-insensitive, trimmed, across both types) returns **409**
  `COLLECTION_NAME_ALREADY_EXISTS`. The item does not move and no collection is
  created.
- A missing or unknown intent returns **400** `INVALID_COLLECTION_INTENT`. Nothing
  is defaulted: the two intents have opposite outcomes for a taken name.

The create path uses the existing `CreateUserCollection` query unchanged. Its
`ON CONFLICT (user_id, lower(btrim(name))) DO NOTHING` reports a taken name as
`pgx.ErrNoRows`, which `createNewUserCollection` maps to the conflict; SQLSTATE
`23505` is never read. No migration or baseline change. The worker's reserved-name
rule is untouched.

**Flutter is not yet updated.** Until it sends `collection_intent`, every move gets
400; it must also map `COLLECTION_NAME_ALREADY_EXISTS` and retire the stale
`ApiFailureCollectionNameReserved` mapping. This is a breaking request change, so the
client must ship with (or before) this backend.

Verification (2026-10-23):

- `swag init -g cmd/api/main.go -parseInternal` — pass; schema adds `collection_intent` and the 409.
- `gofmt` on the changed Go files — pass.
- `go build ./...` — pass.
- `go vet ./...` — pass.
- `go test ./internal/api/collection/... -count=1` — pass.
- `go test ./internal/integration -run 'TestCollection_PutSavedItem|TestCollection_PutSavedItemCreateIntent|TestCollection_CreateUserCollectionConflict|TestCollectionAPI_PutSavedItem' -count=1 -v` — pass.
- `go test ./... -count=1` — pass.

### 2026-10-22 — Any owned collection is a valid move destination

`PUT /saved-items/:id/collection` now resolves the requested name against every
collection the user owns, not only `type = 'user'`. `YouTube`, `Unsorted` and any
other existing system collection are valid destinations; an absent name still
creates a user collection, and movement remains atomic, owner-scoped and idempotent.

Root cause: `GetUserCollectionByNameForUser` filtered `type = 'user'`. When the name
belonged to a system collection, `CreateUserCollection` conflicted with
`collections_user_name_unique` (which spans `(user_id, lower(btrim(name)))` for both
types), the lookup returned no row, and the repository reported the name as reserved.
The lookup now has no type filter; a unique-name conflict resolves to the existing
row, whatever its type.

`COLLECTION_NAME_RESERVED` was removed from the collection API error codes and
Swagger for this endpoint: no collection name is rejected because a system collection
owns it. A no-row result after a conflict now means the row was changed or removed
concurrently, and is reported as an internal failure rather than a client conflict. The
worker's independent reserved-name rule for organization is unchanged and out of
scope.

No migration or baseline schema change was needed. The baseline has no reserved-name
constraint; `collections_user_name_unique` is only per-user name uniqueness, and
`saved_items_collection_id_fkey` enforces the composite ownership guard.

Verification (2026-10-22):

- `sqlc generate` — pass; the generated lookup no longer filters by type.
- `swag init -g cmd/api/main.go -parseInternal` — pass; the old 409 and reserved-name text are gone.
- `gofmt` on the changed Go files — pass.
- `go build ./...` — pass.
- `go vet ./...` — pass.
- `go test ./internal/api/collection/... -count=1` — pass.
- `go test ./internal/integration -run 'TestCollection_PutSavedItem|TestCollectionAPI_PutSavedItem' -count=1 -v` — pass.
- `go test ./... -count=1` — pass.

### 2026-10-18 — Recent saved items and collection counts

Implemented. Two independent changes, both additive, neither altering an existing
field, ordering or pagination rule.

| Endpoint                            | Change                                                        |
| ----------------------------------- | ------------------------------------------------------------- |
| `GET /saved-items`                  | **restored** — the cross-collection recent history, cursor-paginated |
| `GET /collections`                  | each collection now reports `saved_items_count` and `saved_items_count_overflow` |

`GET /saved-items` was removed in `644e612` and is back. It was **not** restored
from git: the removed version was offset-paginated on `ORDER BY created_at DESC`
with eight columns, and all three of those are wrong now. The replacement is
cursor-paginated, ordered `created_at DESC, id DESC`, and reports the full
thirteen-field item plus a nested collection.

#### `GET /saved-items` — the Inbox

```json
{
  "data": { "saved_items": [ {
    "id": "…", "url": "…", "domain": "example.com", "platform": null,
    "title": null, "description": null, "image_url": null,
    "collection_id": "…", "enrichment_status": "pending",
    "last_enriched_at": null, "created_at": "…", "updated_at": "…",
    "collection": { "id": "…", "name": "Wishlist" }
  } ] },
  "message": "saved items retrieved successfully",
  "meta": { "cursor": { "next_cursor": null, "has_more": false } }
}
```

- **Every collection the caller owns, one chronological list.** Unsorted, a
  collection they named and one automatic organization created are all here, and
  each item reports where it lives. This is what separates it from
  `GET /collections/:id/saved-items`, which is unchanged.
- **Nothing is filtered.** `pending`, `completed` and `failed` all appear, and an
  item filed into a collection stays in the history. The item's own
  `enrichment_status` is reported, so a client tells the three apart without any
  of them being withheld.
- **`collection` is inside each item**, not beside the page. The reasoning that
  keeps `type` and `system_key` out of the per-collection listing inverts here:
  that caller arrived knowing the id, and this one cannot. The join is the same
  one `GetSavedItemByIDForUser` already makes, so naming the collection costs no
  extra query and no request per row.
- **Read-only.** No enqueuer is consulted by this path, no transaction is opened
  and nothing is written. Browsing is not a reason to fetch or to organize.
- **Cursor-paginated** with the same conventions as the other two listings:
  default 20, max 50, `limit + 1`, cursor built from the last **returned** row,
  `ListSavedItemsCursor` validated and its position parsed before any query runs,
  explicit `next_cursor: null` on the final page, `[]` rather than null when
  empty. The payload carries **no `Sort` and no `Group`** — one ordering, no
  pinned row — and there is a test asserting it does not.
- **`saved_items_user_created_at_idx ON (user_id, created_at DESC)`** is the
  index behind the ordering. **No migration was created or applied**; the index
  already existed and a tie-group filter on `id` is bounded by the number of rows
  sharing a timestamp.
- **No rate limiter.** This is one indexed read that holds nothing beyond the
  request, exactly like `GET /saved-items/:id` and `DELETE /saved-items/:id`. The
  two saved-item routes that do cost something stay limited, because a save
  arranges unbounded downstream work and an on-demand enrichment fetches in
  process.

Errors: `400 BAD_REQUEST` / `VALIDATION_ERROR` / `INVALID_CURSOR`, the three 401s,
`500 INTERNAL_SERVER_ERROR`.

#### `GET /collections` — bounded counts

```json
{ "id": "…", "name": "Wishlist", "type": "user", "system_key": null,
  "saved_items_count": 12, "saved_items_count_overflow": false,
  "created_at": "…", "updated_at": "…" }
```

- **0–99 exact, `overflow: false`. 100+ reports `99` with `overflow: true`.**
- Computed in the **same statement** as the page, via a `LEFT JOIN LATERAL`
  correlated on the collection's own id and owner. Not a second statement, not a
  pre-aggregated derived table (which would aggregate every `saved_items` row in
  the database on every page), and not one query per collection. One plan, one
  round trip, one snapshot — so a collection can never be reported beside a count
  a separate statement disagreed with.
- An **empty collection reports `0`, never null**, and is still listed.
- Ordering, the pinned-Unsorted rank, all three sorts, cursor groups and
  `has_more` are untouched. Asserted directly rather than assumed.
- The **bound is a parameter**, so `CollectionSavedItemsCountCap` states it once
  for all nine statements rather than nine literals that could disagree.

**`LEFT` and `COALESCE` are defensive, not load-bearing, and this was measured.**
An aggregate with no `GROUP BY` always returns exactly one row, so `COUNT(*)` is
0 rather than null for an empty collection. Replacing either with its inner or
raw form was tried and **every test still passed**. What the pair buys is that a
future change adding a grouping key cannot silently produce a null count or drop
an empty collection from the listing. The observable contract is what is tested.

#### The count is bounded in the rows it reads, and that is measured

The first version clipped the **answer** only — `LEAST(COUNT(*), cap)` over the
whole collection — which bounds what is reported and not what is read. The
aggregate still walked every saved item in the collection and discarded the
result at the end, so a collection of 50,000 cost 50,000 index entries to produce
the same "99+". Nothing in the response distinguishes 100 from 50,000, so none of
that work could change the answer.

Every listing statement now bounds the **rows**:

```sql
FROM (
    SELECT 1
    FROM saved_items si
    WHERE si.collection_id = c.id
      AND si.user_id = c.user_id
    LIMIT sqlc.arg(count_cap)::bigint + 1
) bounded
```

The aggregate expressions are untouched; they now count the bounded set.

**Measured** on a disposable PostgreSQL 18 container against this project's
baseline schema, with one user holding collections of 0, 1, 99, 100, 101 and
50,000 saved items plus a second user with 50,000 of their own. Same statement,
same parameters, one page of 20 collections plus the lookahead row,
`EXPLAIN (ANALYZE, BUFFERS)`, all buffers `shared hit`:

| | Rows examined | Buffers | Execution time |
|---|---|---|---|
| unbounded `COUNT(*)` | 7,186 avg × 7 loops | 1,035 hit | **7.211 ms** |
| bounded to `cap + 1` | 57 avg × 7 loops | 27 hit | **0.125 ms** |

`saved_items_collection_id_idx` is confirmed as the index in both plans
(`Index Scan using saved_items_collection_id_idx`, `Index Cond: collection_id =
c.id`), with the new `Limit` node sitting above it. The scan is bounded rather
than removed: 57 average is the mean across collections of 0, 1, 99, 100, 101 and
50,000, and no collection can exceed 100.

The two forms were also run side by side and agree row for row on all seven
collections, including the two that the `+ 1` exists for: **99 → `99`/`false`
and 100 → `99`/`true`** in both forms.

**What was not improved, stated plainly:** the plan is an index scan *with a
filter*, not an index-only scan, because `si.user_id` is not in
`saved_items_collection_id_idx` and the predicate needs the heap tuple. Dropping
`AND si.user_id = c.user_id` would make it index-only — migration 000025's
composite foreign key guarantees it cannot change the result — but that removes a
defence-in-depth predicate and is a separate decision, not part of this change.

The `+ 1` is mutation-tested: dropping it so the limit is exactly `cap` fails
four cases, because a collection holding exactly 99 would then be reported as
`99`/`false` and one holding 100 as `99`/`false` too. No new tests were needed;
the existing cases for 0, 1, 99, 100, 101 and cross-user isolation already
distinguish every state this expression can produce.

#### Verification, and that it detects what it claims

`internal/api/saved_item` — 24 new tests: limit defaulting, `limit=0` read as
absent, clamping, user scoping, one call and nothing else, pass-through of
repository errors; **eleven** unusable cursor shapes each refused as the same
`400 INVALID_CURSOR` with `Times(0)` on the repository so a bad token provably
spends no query; a valid cursor handed over as a parsed `pgtype.Timestamptz`;
the next cursor built from the last returned row; absent on an empty and on a
final page; the payload's JSON keys asserted to contain neither `s` nor `g`.

`internal/api/collection` — 9 new tests on the response contract: `0`, `1`, `99`
and `99`+overflow reaching the wire unchanged; both keys present where they carry
their zero value; an empty listing serializing as `[]`.

`internal/integration` — 12 count cases against real rows (`0`, `1`, `99`,
`100`, `101`, `297`, Unsorted, an automatic system collection, another user's
items excluded, the whole collection on every cursor page, all three sorts across
all three resume points, ordering and pinning unchanged) plus 8 pagination and
behaviour cases for the new endpoint (three collections in one chronological
list, an item kept after being filed through the real move endpoint, all three
enrichment states, another user's items excluded, `[]` when empty, nullable keys
present and null while `domain` is correctly *not* null, three consecutive reads
leaving every column byte-identical, deterministic ordering across 12 rows
sharing one `created_at`, a 45-item walk over three collections with no duplicate
and no gap, resuming after the anchor is deleted, a cursor past the end, eleven
rejected cursors, `limit=0`, and `51`/`100`/`-1` refused while `1`/`50` are
accepted).

**Two new documented-shape guards**, because adding fields to a documented
response with no guard is exactly how `meta` once disappeared from a schema:
`TestCollectionAPI_ListCollections_DocumentedShape` walks `docs/swagger.json`
and a real body side by side and fails if either gains or loses a key, asserting
both count keys and their types; `TestSavedItemAPI_ListRecent_DocumentedShape`
does the same for the new endpoint down to the per-item collection object.

**One existing test asserted the opposite of this change and had to be replaced.**
`TestSavedItems_ListRouteIsGone` pinned `405 Method Not Allowed` on
`GET /saved-items`, i.e. that the route was *not* registered. That is exactly the
assertion the new requirement invalidates, so it could not be left passing. It is
now `TestSavedItems_ListRouteIsDistinctFromTheItemRoute`, which keeps the half
that still matters: registering a collection-less listing on the same group as
`GET /saved-items/:id` puts a static path beside a parameterised one, and a router
resolving them in the wrong order would answer every item request with an empty
list. The replacement still asserts `GET /saved-items/:id` returns `404` on a
missing id, which is what holds that apart.

**Mutation-tested.** Each guard was removed in turn and the suite re-run:

| Mutation | Result |
|---|---|
| `LEAST(COUNT(*), cap)` → `COUNT(*)` | **fails** in 8 cases — reports 100, 101, 297 instead of 99 |
| `COUNT(*) > cap` → `FALSE` | **fails** in 4 cases — overflow never set |
| first-page `ORDER BY` loses `si.id DESC` | **fails** — ties come back unordered |
| an `enrichment_status` filter added | **fails** in 9 cases |
| `LEFT JOIN LATERAL` → `INNER JOIN LATERAL` | **passes** — so the comment says so rather than claiming protection it does not give |
| `LIMIT cap + 1` → `LIMIT cap` | **fails** in 4 cases — 100 items reports `false` overflow |

The last row is why that paragraph reads the way it does.

Commands run, and their results:

```sh
sqlc generate                                     # exit 0
swag init -g cmd/api/main.go -parseInternal       # exit 0
go build ./...                                    # exit 0
go vet ./...                                      # exit 0
gofmt -l <the 27 changed .go files>               # clean
go test ./... -count=1                            # exit 0, 23 packages
```

`gofmt -l` over the whole tree is **not** clean, and was not before this change:
about sixty files this work never touched are reported for CRLF line endings,
which is a Windows checkout artefact rather than a formatting fault. The check
that means something here is gofmt over exactly the files that changed, which is
clean.

**The race detector was not run.** These are SQL, pagination and response-shape
changes with no new concurrency; `AGENTS.md` asks for it on changes that add or
alter concurrent code, and there is none here.

**No migration was created or applied.** `saved_items_user_created_at_idx` and
`saved_items_collection_id_idx` already exist and are the indexes behind the new
statements. `internal/database/baseline/schema.sql` is untouched.

**Nothing was committed.**

### 2026-10-15 — User Service: profile and password credential

Implemented. `internal/api/user/` is a complete vertical slice, registered as a
**sibling** of `auth` in `cmd/api/main.go`.

| Endpoint              | Status | Notes                                             |
| --------------------- | ------ | ------------------------------------------------- |
| `GET /users/me`       | done   | 200, authenticated only, no caller-supplied user id |
| `PATCH /users/me`     | done   | 200, `display_name` only, returns the written row  |
| `PUT /users/me/password` | done | 200, creates or changes depending on what the rows hold |

**No route accepts a user id.** There is no `:id` parameter and no `user_id` in
any query or body, so "read somebody else's profile" is not a request this API
can express. Every handler reads the id from the verified token's `sub` claim and
the current session from its `sid` claim, through a safe two-value type assertion
rather than the unchecked form most handlers in this repository use.

**The password hasher is now built in `cmd/api/main.go`** and passed to both
`auth.RegisterModule` and `user.RegisterModule` as the same value. This is the
whole cost of the feature being a sibling rather than a submodule:
`security.NewPasswordHasher` wraps the process-wide `PasswordWorkLimiter`, so a
hasher built from a second limiter would be a second *allowance* rather than a
second view of one, and the derivations running at once would be the sum.
`internal/api/auth/module.go` now receives the hasher instead of building it.

### What `GET /users/me` reports, and what it refuses to

```json
{ "data": { "user": {
    "id": "…", "email": "…", "display_name": "…",
    "has_password_credential": true,
    "created_at": "…",
    "identities": [ {
        "provider": "google", "linked_at": "…",
        "email_snapshot": "user@example.com",
        "display_name_snapshot": "John Doe"
    } ]
} }, "message": "user retrieved successfully" }
```

- **`email_verified_at` is deliberately absent.** The column is `NOT NULL` with no
  default, so every account has a value and reporting it presents a constant as
  information. There is no verification flow for a client to gate on.
- **`provider_subject` is never reported.** It is the credential the identity is
  bound to at the provider, and the row id is an internal identifier no endpoint
  addresses.
- **`provider` is the stored string, verbatim.** The column is `VARCHAR(32)` with
  no check constraint and no registry, and the only value written today is
  `google`. A client must render one it has not seen rather than switching over a
  set the server maintains.
- **`identities` is always an array, never null**, ordered `created_at ASC, id ASC`.
- **The session id is deliberately not returned.** The caller's own session is
  already in the verified token, and publishing it would create a second,
  client-echoable copy of a security identifier where no endpoint accepts one
  today. `GET /auth/sessions` answers "which device am I on" with the device name,
  the platform and an `is_current` flag, so nothing is lost.

#### The snapshots are history, and that is the whole point of them

`email_snapshot` and `display_name_snapshot` are what the provider asserted about
the account **at the moment the link was made**. They are reported as stored, which
means three things follow and all three are deliberate:

- **They are not copies of the account's own email and display name, and are not
  reconciled with them.** Migration `000026` removed the duplicate address from
  `account_link_confirmations` for exactly this reason — the account's own email is
  the authoritative one. A snapshot that has drifted from the account is a fact
  about *when the link was made*, not an inconsistency for this endpoint to
  correct. `PROGRESS.md` records the same reasoning for `auth_identities`
  generally: nothing has ever refreshed these columns, and they are not tied to
  `users.email` by any foreign key or trigger.
- **Nothing is fetched from the provider to update them.** Deciding what to do
  about a stale snapshot belongs to the frontend; an endpoint that contacted a
  provider on every profile read would make reading a profile a network call.
- **Both are nullable, and nullability is preserved.** `pgtype.Text` in the
  projection, `*string` in the response, and `Valid` — not `String` — is what the
  mapper tests. A provider that asserted **nothing** reports JSON `null`; a
  provider that asserted an **empty value** reports `""`. Those are different
  facts, and collapsing them would tell a client the provider said something when
  it did not. Neither is ever filled in from the account's current profile.

The keys are always present on every identity, including the null ones, so a client
can read one without first checking whether it arrived.

**Mutation-tested.** Replacing the `Valid` check in `nullableText` with an
unconditional `&value.String` fails both the mapper unit test and
`TestGetProfile_SnapshotsPreserveStoredNullability`, so the distinction is asserted
rather than merely intended.

### `PUT /users/me/password` is one endpoint, and the database decides

The endpoint serves both cases and never trusts the client about which it is.
`has_password_credential` is a rendering hint; the service asks the database and
applies the rule it finds.

| Case | Behaviour |
|---|---|
| No credential | `current_password` must be absent — supplying one is `400 UNEXPECTED_CURRENT_PASSWORD`, not silently ignored. One Argon2id `Hash`. Plain `INSERT`. **Revokes nothing.** |
| Credential exists | `current_password` required (`400 CURRENT_PASSWORD_REQUIRED`). `Verify` → `Hash` → conditional `UPDATE` + revocation in one transaction. Revokes every other session. |

**Creating a first password revokes nothing, and that asymmetry is deliberate.**
Nothing was compromised — there was no credential to compromise. Evicting every
other signed-in device of an account using Google sign-in on a phone and a laptop
would punish its owner for adding a password. Only the branch that *replaces* a
secret evicts, because that is the one an owner might replace because they believe
it was seen.

**The create branch uses a plain `INSERT`, and this is not a style choice.** The
password-reset flow writes the same table with `INSERT … ON CONFLICT DO UPDATE`,
which is correct there and wrong here: reset has already proven mailbox control and
does not need to know whether a credential existed. Under an upsert two concurrent
creators **both succeed** and the second silently overwrites the first — both
callers told they had set the password they submitted. Under an `INSERT` the loser
loses on `password_credentials_pkey` and is told `409
PASSWORD_CREDENTIAL_ALREADY_EXISTS`.

**The change branch's write is conditioned on the hash it verified.**
`UpdatePasswordCredentialIfUnchanged` carries `previous_password_hash` — the value
read and verified — in its `WHERE`. Without it two concurrent changes from one
session both write and the last wins invisibly; with it the second matches no row
and is refused `409 PASSWORD_ALREADY_CHANGED`. The guard sits **before** the
revocation inside the transaction, so a request whose credential was replaced by a
concurrent one never signs the account's other devices out of a change it did not
make.

**Neither the pre-read nor the branch choice authorizes anything.** Both are
values, not permissions: the authority is the `INSERT` that the primary key
refuses, or the conditional `UPDATE` that returns zero rows. No `FOR UPDATE` and no
check-then-act, so there is no lock-ordering to reason about.

**Hashing and verification happen outside the transaction.** A
`503 PASSWORD_WORK_UNAVAILABLE` therefore writes nothing at all — no credential, no
revocation — which is the property `password_reset` established and which is
asserted by gomock strictness on every refusal path.

**`DummyVerify` is deliberately absent.** It exists so an unauthenticated login
against an unknown address pays the same Argon2id cost as a known one. The caller
here is authenticated and already knows whether it holds a credential, so there is
no unknown-account branch to equalize.

### Revocation does not take effect immediately — and the docs say so

Changing a password writes `sessions.revoked_at` for every other session and
preserves the caller's, in one transaction with the credential write. **It does
not invalidate an already-issued access token.** Access tokens are stateless and
`middleware.Auth` never consults the database, so a token issued before the change
keeps working until its own expiry — up to `AccessTokenLifetime`. This is the
documented trade-off in `documentation/auth-sessions.md` and
`AGENTS.md`, not a gap introduced here.

Two consequences recorded so neither is "fixed" later: the Swagger description
names it explicitly, and `TestUserSwagger_DoesNotPromiseImmediateTokenInvalidation`
asserts the description says "stateless" and "15 minutes". **Preserving the
caller's own session grants an attacker nothing** — a caller reaching this endpoint
already holds a live token for it — and revoking it would log the legitimate user
out of the tab they made the change in.

### Verification, and that it detects what it claims

`internal/api/user` — 31 unit tests, including the password-work capacity mapping
(503, the literal message shared with login/registration/password reset, a bounded
`Retry-After`, and *not* 429/400/401/500), a cancelled context staying a 500, the
same request succeeding once capacity returns, and — through gomock strictness —
that a wrong current password costs exactly one derivation and never reaches
`Hash`, and that the create branch never reaches `Verify`.

`internal/integration` — 38 tests against real PostgreSQL: the profile key set
pinned exactly, no sensitive field in the marshalled bytes, no identities as `[]`
rather than null, both registration length boundaries accepted and one outside each
rejected, `users_set_updated_at` observed to fire, a supplied `user_id` changing
nothing, a created credential signing in through the **real** `POST /auth/login`,
a changed credential stopping the old password working through it, other sessions
revoked and the caller's preserved, and both concurrency cases.

**Two concurrency tests were proven to detect the defects they name.** Each guard
was removed in turn and the suite re-run:

| Mutation | Result |
|---|---|
| `CreatePasswordCredential` → `INSERT … ON CONFLICT DO UPDATE` | **fails** — 4 successes instead of 1; the silent overwrite |
| conditional `UPDATE` → predicate always true | **fails** — 0 successes instead of 1; the lost update |

**The create-race test uses four participants, not the sixty-four the
registration-creation race uses, and the difference is reasoned rather than
copied.** That race guards a window between a locking `SELECT` returning no rows
and the `INSERT` — microseconds. This one guards a window that is **one full
Argon2id derivation** wide, which is hundreds of milliseconds. Sixty-four
participants cost sixty-four derivations against a concurrency-4 test limiter and
timed the test client out, turning the status code under test into a transport
error. Four contend reliably in a window that wide. `doUserRequest` now passes an
explicit `fiber.TestConfig.Timeout` for the same reason.

**The lost-update guard is tested against the repository, not through HTTP.** Its
window lives *inside* a request — between the hash read and the `UPDATE` — and
cannot be opened from outside the service: any write made before the request
begins is the hash the request will read, and so is the one it will match. The
first version of that test seeded an intervening hash from outside and passed
against broken code for exactly that reason. Calling
`SetPasswordAndRevokeOtherSessions` twice with the same stale hash is the honest
version, and it also asserts the rollback: the refused call writes nothing and
revokes nothing.

Commands run, and their results:

```sh
go build ./...                                    # exit 0
go vet ./...                                      # exit 0
gofmt -l internal/api/user internal/integration    # clean
sqlc generate                                     # exit 0
swag init -g cmd/api/main.go -parseInternal       # exit 0
go test ./... -count=1                            # exit 0, no failures
go test -race ./... -count=1                      # exit 0, no DATA RACE
go test ./internal/integration/ -run 'Concurrent|ConditionalUpdate' -count=8
                                                  # no flake
```

Swagger is **purely additive**: `docs/docs.go`, `docs/swagger.json` and
`docs/swagger.yaml` are 837 insertions and 0 deletions between them.

### No migration, and the index that was deferred

**None was created or applied.** Everything works against the current schema.

`auth_identities.user_id` still carries **no index** — it is the only FK column in
the user/identity schema without one, and `ListAuthIdentitiesForUser` therefore
scans that table on every profile read. It is a small table and the decision to
defer it was taken deliberately; it is recorded here so it is not rediscovered as
a surprise. `password_credentials.user_id` is the primary key and
`sessions` has `sessions_user_active_idx ON (user_id, last_activity_at) WHERE
revoked_at IS NULL`, so the revocation statement is served directly by an index
and needs nothing added.

### Rate limiting — implemented

`PUT /users/me/password` carries a per-user budget. The two profile routes
deliberately carry none.

| Route                     | Namespace           | Policy      |
| ------------------------- | ------------------- | ----------- |
| `GET /users/me`           | **none**            | —           |
| `PATCH /users/me`         | **none**            | —           |
| `PUT /users/me/password`  | `user-password-user` | 5 / 15 min |

Mounted with `middleware.UserRateLimit` between `Auth` and the handler, the same
place and shape as `search-user` and the two saved-item budgets. The subject is
`claims.Subject`, because the caller is already authenticated and the work being
bounded is Argon2id work against a pool shared with the whole deployment. It is
deliberately **not** per-address, unlike `finalize-ip` and
`password-reset-finalize-ip`, which are per-address because those routes are
reachable without a credential at all.

**Why 5/15 and not the 10/15 both single-derivation finalize routes use.** The
principle recorded at `password_reset/constants.go` is that those two match "because
both routes perform one derivation per request and cost the same to serve" — and
that is conditional on the cost matching. **Changing a password performs two
derivations** (verify the current one, hash the replacement) where creating a first
one performs one, so the count is halved rather than the inconsistency introduced.
The 15-minute window is copied for the reason `registration/constants.go` gives: a
single request here is among the most expensive in the application, so the budget
stays generous per unit of work.

**No `Release` and no failure-only counter.** `login-email` charges optimistically
and refunds on success so a mistyped password costs nothing. The situations are
inverted here: at login a success is the common case and a failure the exception,
whereas a *failed* password change is exactly what this budget exists to bound,
because the change branch verifies a real secret.

**The two profile routes are unlimited, and that is the availability argument
rather than the cost one.** They spend one indexed read, one primary-key probe and
one `auth_identities` read, and hold nothing beyond the request. `UserRateLimit`
**fails closed**, so budgeting them would mean that during a Redis outage the
application could not render a profile or rename itself — bought for no protected
resource. The repository already accepts that failure mode where it matters, on
`POST /auth/refresh`, and takes it out on a read is a different trade. A rename
writing is not itself a reason either: `DELETE /saved-items/:id` and
`DELETE /collections/:id` both write and are both unlimited, and the distinction
`saved_item/routes.go` draws is that a save is budgeted because it *arranges*
unbounded downstream work.

**The namespace is its own, and spends no other flow's budget.** A user who has
spent their password allowance must still be able to sign in and to open a password
reset — which is the recovery path when this budget refuses them. Both are asserted
against the real counter in `internal/integration/user_password_rate_limit_test.go`.

**All thresholds are initial engineering estimates, not production-validated
values.** No deployment has been selected and no traffic observed, which is the
standing caveat on every budget in this repository.

### Not done here

- **No `GET /users/me/password`.** The only thing it could report is
  `has_password_credential`, which the profile already carries.
- **Email change, email verification, account deletion, avatars, provider
  unlinking, a `/users/:id` surface.** All out of scope.
- **No refresh of the identity snapshots.** They are historical metadata from link
  time. Nothing in this application refetches them from a provider, and adding
  that would be a new feature rather than a response-contract change.
- **The password-length tags remain three different declarations** —
  `required,min=8` in registration and password reset, `required,min=8,max=128` in
  login. The new endpoint matches the stricter one so a password login accepts
  what it accepts; aligning the others is separate work and was not done here.
- **`auth_identities.user_id` index**, as above.
- **Nothing was committed.**

### 2026-10-15 — Liveness endpoint renamed `/health` → `/livez`

The route is now **`GET /livez`**. The response contract is unchanged — `200` and
`{"status":"ok"}` — and `GET /health` no longer answers. There is no alias: two
live endpoints with the same meaning invite wiring the platform to the one whose
name you recognise.

The rename is the point. `/health` reads as a statement about the application;
`/livez` says what it actually reports, which is only that the process is running
and able to serve HTTP. A `200` from it is fully compatible with an unreachable
database or Redis.

**The handler stays dependency-free, and that is the decision worth recording
rather than the rename.** This application's limiters fail closed by choice, so an
unreachable Redis already stops PIN issuance, manual login, Google
authentication and token refresh (see "Outstanding deployment work" item 3). If
`/livez` also checked Redis or PostgreSQL, that same outage would fail the probe,
and a platform restarting or draining instances on a failing health check would
turn a service that is degraded but serving into one that is entirely
unavailable — at exactly the moment it is already impaired. Restarting a process
does not restore an external dependency. So a readiness probe, when a hosting
platform is chosen, has to be **a separate endpoint**, not this one with a
database check added.

Startup already answers the readiness question earlier and more strictly:
`database.NewPool` pings PostgreSQL and both limiter `Check` calls verify Redis,
`main` returns rather than serving if any of them fails, and the listener does not
open until every module is registered. An instance that answers `/livez` at all
has therefore already passed every startup check.

The endpoint is still unauthenticated — a platform calls it holding no
credential — and is still absent from Swagger. `cmd/worker` has no HTTP surface
and so has no liveness probe; that stays outstanding until a platform is chosen,
since giving the worker a listener would change a binary the architecture
documents as having none.

### 2026-10-15 — Session and refresh-token cleanup

Implemented. Reference: **`documentation/auth-sessions.md`**, section "Session
cleanup" — read it before changing the predicate, the batching, or the guard.

`POST /internal/maintenance/sessions/cleanup` removes sessions that are revoked,
past `absolute_expires_at`, or past the idle window, and lets the existing
`ON DELETE CASCADE` take each one's `refresh_tokens` chain with it.

**The predicate is the refresh path's own three conditions**, read from the row
rather than decided a second time, so cleanup cannot remove a session refresh
would still accept. `SessionIdleLifetime` is the only source of the idle
duration — the repository takes it as a parameter and binds it as an interval,
and no query file contains a literal `30 days`. Cleanup evaluates all three
against `NOW()`; the runtime refresh path still compares against Go's
`time.Now()`, and that difference is deliberate and recorded.

**Only `sessions` rows are deleted.** No statement anywhere deletes an individual
refresh token while its session survives: `replaced_by` is `ON DELETE SET NULL`,
so removing one token out of a chain would leave its predecessor looking
unrotated and silently disable reuse detection for that chain.

**Migration `000028_add_refresh_tokens_replaced_by_index` adds
`refresh_tokens (replaced_by)`, and it was applied by hand before this change.**
`internal/database/baseline/schema.sql` now carries the same definition, so
throwaway test databases match. The index is not for the cleanup predicate: it is
for the cascade. `replaced_by`'s `SET NULL` action is a sequential scan of
`refresh_tokens` per deleted token row without it. Measured on a disposable
PostgreSQL 18 container against this project's baseline schema — same statement,
warm cache, all buffers shared hits, deleting 100 sessions / 500 token rows from
a 250,000-row table: **8,205 ms as-is, 4.5 ms with the index.** The same cascade
is reachable today via `sessions.user_id → users(id) ON DELETE CASCADE`, so the
hazard was latent rather than introduced.

**No index was added to `sessions`.** The three conditions are an `OR`, which no
single-column index can serve: with indexes on both `last_activity_at` and
`absolute_expires_at` present, the planner used neither and walked
`sessions_pkey`. At 550,000 sessions with 6% eligible, finding 500 candidates took
2.8 ms. `documentation/auth-sessions.md` records what production evidence would
justify revisiting this, and notes the accepted limitation that the statement may
scan many rows.

**Batching.** `SessionCleanupBatchSize = 500` and `SessionCleanupMaxBatches =
100`. Each batch is its own statement and its own transaction, so a later failure
leaves earlier batches committed — and the failure log reports that committed
count rather than zero. `backlog_capped` means "stopped because it hit the cap",
not a backlog size; no query is run to count what is left. Both constants are
estimates. **The schedule is external and nothing in this process sets it**; the
intended cadence is once per day.

**The guard is a private function in `session/routes.go`**, not a new middleware —
it has one caller and none of the concepts `internal/middleware` models apply to a
route holding no user credential. `SESSION_CLEANUP_SECRET` is **optional**;
`config.Load` still requires the five secrets nothing works without. Absent,
empty, wrong-case, wrong-length and unconfigured secrets all answer the same
`401 CLEANUP_SECRET_INVALID` with the same message, the comparison is
`subtle.ConstantTimeCompare`, the underlying error is `nil` so the shared error
handler cannot log the value, and there is deliberately **no rate limiter** — the
budgets fail closed on Redis, which is wrong for an operator wanting to run
cleanup while something else is broken. The startup line records
`session_cleanup_enabled` as a boolean.

`.env.development` gained a local `SESSION_CLEANUP_SECRET` placeholder. The file
is gitignored and untracked, the value is deliberately fake, and it is not
reproduced in any tracked file — `git grep` for it returns nothing.

One behavioural change worth knowing: a revoked session that has already been
reclaimed answers `REFRESH_TOKEN_INVALID` rather than `SESSION_REVOKED`, because
its token row is gone. Both are `401`.

**Nothing was committed, and no migration was executed by this change.**

### 2026-10-14 — Shared PIN email budgets for registration and password reset

Implemented. Reference: **`documentation/pin-email-budgets.md`** — read it before
changing any part of this policy.

Both features that send mail now draw on two budgets they share, enforced with
Redis-backed rolling windows: **10 admissions per normalized address per 24
hours** and **250 deployment-wide per 24 hours**, covering initial issuance and
resend in both features. They replace registration's previous fixed-window
`pin-email` budget of five per hour, and add to password reset, which had no
per-address budget at all.

What changed:

- `internal/ratelimit/sliding.go` — a new `SlidingLimiter`. **`Limiter.Allow` is
  untouched**; this is a second mechanism with its own script, result type and
  `rlw` key prefix.
- `internal/email/budget.go` — `PINEmailBudget`, owning the namespaces, the
  policies, and the one rule about what may be refunded.
- `internal/config/email_budget.go` — `PIN_EMAIL_BUDGET_PER_EMAIL_MAX`,
  `PIN_EMAIL_BUDGET_GLOBAL_MAX`, `PIN_EMAIL_BUDGET_WINDOW_SECONDS`, range-checked
  at startup. Defaults 10 / 250 / 86400.
- `email.Sender` is **unchanged**. What was added is `email.NotAttempted` /
  `email.Attempted`, a marker for a sender that could not have reached a
  provider. `UnconfiguredSender` now reports it, so a build without a provider no
  longer drains the deployment budget by failing to send. `DevSender`
  deliberately does not, because it stands in for a provider that was reached.
- Ordering is now cooldown → per-address budget → global budget → issue → send.
  Only the global budget is refunded, and only for failures strictly before the
  provider could have been contacted.
- `PIN_RATE_LIMIT_EXCEEDED` keeps its string. New: `PIN_EMAIL_BUDGET_EXCEEDED`,
  `PIN_EMAIL_BUDGET_UNAVAILABLE`, and `PIN_EMAIL_DELIVERY_FAILED` — the last is a
  503 with a `Retry-After`, where this path used to return a 500.
- IP budgets, their policies and trusted-proxy behaviour are **unchanged**.

**The three thresholds are initial engineering estimates, not measured values.**
No deployment is chosen and no traffic observed; 250 is placed below a candidate
provider plan's quota, not derived from it. See the caveats with the defaults in
the reference document's "What they bound" section.

**No provider was selected or integrated.** Brevo remains a candidate. **No
migration was created or applied**, and nothing was committed.

## Current Status

### Registration lifecycle

Pending manual and social registrations are no longer reused after their
verification request has completed. Manual registrations are replaced only once
no usable continuation remains; social registrations are replaced for the same
verified Google identity, after the identity-mismatch refusal. Password reset is
unchanged: its reset and continuation expire together, so the existing
expiry-based recovery is the safe one and the request path must keep returning
the existing verification id while the continuation is live.

`POST /auth/register/manual` now reports `verification_status`. `pending` means
request a PIN; `verified` means the PIN step is already complete and the client
must use its stored continuation token instead. The backend cannot reissue a lost
token: retrying after the 15-minute continuation lifetime replaces the
registration and returns `pending`.

Remaining client-side work: decode `verification_status` (default to `pending`
when talking to an older backend), persist the raw continuation token immediately
after `/verify`, and branch to the continuation/finalization flow on `verified`.
No backend work remains for these fixes.

### Saved Item lifecycle — end to end

| Stage                                                 | Status  |
| ----------------------------------------------------- | ------- |
| Save (`POST /saved-items`)                            | done    |
| Background enrichment after save                      | done    |
| Background automatic organization                     | done    |
| On-demand enrichment (`POST /saved-items/:id/enrich`) | done    |
| Collection filing (`PUT /saved-items/:id/collection`) | done    |
| Saved item ownership constraint (migration 000025)    | applied |

Migration 000025 is applied to the development database, and
`internal/database/baseline/schema.sql` reflects it, so integration tests exercise
the same ownership constraint production does.

Migration **000026** (`drop_email_snapshot_from_account_link_confirmations`) is
likewise applied to the development database, and the baseline reflects it. The
column existed only as a second copy of an address the confirmation's own join
already yields; the account-link section below describes what replaced it.

The full path — save, background enrichment, and the automatic organization the
enrichment hands off to — was also verified end to end against the running stack
with a manual request walkthrough. Automated tests cover the same flow.

### Saved Items — Phase A backend complete

All four core Saved Item endpoints are implemented, wired into `main.go`
through `saved_item.RegisterModule`, and covered by tests.

| Endpoint                  | Status | Notes                                                    |
| ------------------------- | ------ | -------------------------------------------------------- |
| `POST /saved-items`       | done   | 201, requires Bearer, minimal response                   |
| `GET /saved-items`        | done   | **restored 2026-10-18** — 200, the cross-collection recent history, cursor-paginated |
| `GET /saved-items/:id`    | done   | 200, owner-scoped, complete item + collection            |
| `DELETE /saved-items/:id` | done   | 200, hard delete, owner-scoped, reports collection state |

Update/edit (`PATCH` or `PUT`) is deliberately out of scope for now.

`GET /saved-items` was removed in `644e612` and **restored** in 2026-10-18. The
original decision that there should be no standalone listing is superseded — see
decision 19g and "Recent saved items and collection counts" above. `GET /saved-items`
is now the cross-collection recent history: every saved item the user owns, newest
first, cursor-paginated, with a nested `collection` per item. Every saved item
still belongs to exactly one collection, and `GET /collections/:id/saved-items`
remains the way to see one collection's contents. `Unsorted` is a collection, not
the Inbox, and is never a substitute for the history.

Feature package: `internal/api/saved_item/`, following the project's
vertical-slice convention.

### Save response is minimal, detail response is complete

The two read paths answer different questions and deliberately report different
things.

`POST /saved-items` returns only what a save can meaningfully know:

```json
{
  "data": { "saved_item": { "id": "…", "url": "https://example.com/a", "collection_id": "…", "enrichment_status": "pending" } },
  "message": "saved item created successfully"
}
```

A save commits before enrichment has run, so `domain` is the only metadata column
carrying a value and it was derived locally from the submitted URL, not read off the
page. Reporting `title`, `platform`, `description`, `image_url` or `last_enriched_at`
there would be a set of nulls presented as though they had been looked up, which reads
as "this page has nothing" rather than "this page has not been read yet".

`enrichment_status` is included because the `INSERT` returns it. It costs no extra
query, and reporting the stored value beats reporting a constant the code assumes: it
is what the column's default actually wrote.

`GET /saved-items/:id` returns the complete item plus the collection it is filed in:

```json
{
  "data": {
    "saved_item": {
      "id": "…",
      "url": "https://example.com/a",
      "domain": "example.com",
      "platform": null,
      "title": null,
      "description": null,
      "image_url": null,
      "collection_id": "…",
      "enrichment_status": "pending",
      "last_enriched_at": null,
      "created_at": "…",
      "updated_at": "…"
    },
    "collection": { "id": "…", "name": "Wishlist" }
  },
  "message": "saved item retrieved successfully"
}
```

- **`data.saved_item` matches the listing shape** in `GET /collections/:id/saved-items`
  field for field, for the same reason: without `enrichment_status` a null title is
  ambiguous between "this page has no title" and "this page has not been read yet".
- **`data.collection` carries `id` and `name` only.** `type` and `system_key` belong
  to `GET /collections`: they describe how a collection came to be, which plays no
  part in showing one item. Unsorted arrives through the same object as anything else.
- **`saved_item.collection_id` always equals `collection.id`.** The redundancy is
  deliberate: it makes the item self-describing once it is carried out of the response.
- **No extra query for the collection.** `GetSavedItemByIDForUser` joins
  `collections` in the same statement, owner-scoped on both sides, so naming the
  collection costs nothing beyond what the read already did. The join is `INNER`
  because `collection_id` is `NOT NULL` and references an existing row.
- **A pending or failed item is returned in full.** `title`, `description`,
  `image_url`, `platform` and `last_enriched_at` are reported as JSON `null` with the
  key present, never omitted.
- **Neither DTO was weakened.** `CreatedSavedItemResponse` and
  `SavedItemDetailResponse` are separate types; the detail response did not have to
  give up fields so the create response could be short.

`SavedItem`, the feature's projection, is now the complete row including
`collection_id` and the enrichment columns, because `Get` has to report all of it.
Create and Get are its only readers, so a narrower projection would only mean a
second near-identical type. The collection slice keeps its own separate projection,
shaped for paging over a collection rather than describing one item.

### Authentication and ownership

- All Saved Item endpoints require a Bearer access token via `middleware.Auth`.
- `user_id` always comes from the token's `sub` claim, never from the request.
- Saved Item reads and deletes are owner-scoped by `id AND user_id`.
- Missing and foreign items both return `RESOURCE_NOT_FOUND` / 404, so the API
  does not disclose whether another user's ID exists.
- Invalid UUID path parameters return 400 `BAD_REQUEST`.

**`documentation/auth-sessions.md` is the reference** for the session feature:
how access tokens, refresh tokens and the session row relate, which guarantees
the database enforces and which are only application convention, what `FOR UPDATE
OF rt, s` is locking and why, the accepted trade-offs, and the testing strategy.
Read it before changing rotation, reuse detection, revocation or session expiry.

### Auth session architecture — documented, ordering tie-breaker added

`documentation/auth-sessions.md` was written as a reference for the session
feature and linked from "Authentication and ownership" above.

The refresh-token rotation design is **unchanged and was not reopened**. A review
of the feature found no defect in it; the gaps it found were missing test
coverage, which a separate change closed. Two items have been acted on since:

- **`GET /auth/sessions` ordering now has a unique tie-breaker — implemented.**
  `ListSessions` orders `last_activity_at DESC, id DESC`. `last_activity_at` is
  not unique — it defaults to `NOW()`, which is constant within a transaction, and
  refresh rewrites it with `NOW()` too — so a tied group was previously free to
  come back in any order, and under `LIMIT`/`OFFSET` that is how a client paging
  a device list could be shown one session twice and another not at all while the
  dataset never changed. `sessions.id` is `uuidv7()` and unique, and it is also the
  tie-breaker `AGENTS.md` already requires for the collection listings.
  **This does not make offset pagination stable against concurrent activity**:
  the sort key is still mutable, so refreshing on any device can reorder rows
  between two page requests. Closing that needs an immutable key or keyset
  pagination, and converting this endpoint to a cursor remains a separate product
  decision recorded as 19f. See "Session listing ordering" in the reference
  document.

- **`POST /auth/refresh` now has a per-client-address budget — implemented.**
  Sixty requests per ten minutes under a new `refresh-ip` namespace, mounted as
  `middleware.IPRateLimit` in front of body binding and in front of the handler.
  `session.Routes` takes the existing `auth.RateLimiter` and `auth.RegisterModule`
  passes the one `pinRateLimiter` instance the process already builds, so there is
  no second Redis connection, secret or limiter.

  The reason is **rotation chaining**, and it is the one thing about this route
  that the other session routes do not have. A caller holding a single stolen
  refresh token can present it, receive its successor, and present that; always
  using the newest token, they never present one with `replaced_by` set, so reuse
  detection never fires against them. Every such request is legitimate as far as
  the service can tell and writes a `refresh_tokens` row that nothing removes. The
  chain is bounded only by the 90-day absolute lifetime. A **token-keyed** budget
  would be defeated by that same chaining, and a **user-keyed** one is not
  available: the user id is only known from the locked row read inside the refresh
  transaction. IP is the only subject that works.

  `refresh-ip` is independent of `login-ip` and `google-auth-ip`, so an exhausted
  refresh budget can never lock anyone out of signing in — asserted in both
  directions.

  **Sixty in ten minutes is an initial engineering estimate, not a validated
  limit.** It is derived from `AccessTokenLifetime` — 0.67 refreshes per ten
  minutes per device, so roughly ninety devices at baseline on one address — and
  matches `password-reset-status-ip`, which exists for the same shape of route. No
  deployment is selected and no traffic observed. The assumption it rests on is the
  population behind a shared address; the assumption most likely to require raising
  it is client retry behaviour, which lives outside this repository.

  **Fail-closed couples refresh to Redis.** During an outage `/auth/refresh`
  answers `503 IP_RATE_LIMIT_UNAVAILABLE` and rotates nothing, and a user whose
  access token has since expired cannot renew until Redis recovers — up to 15
  minutes of grace from the outage, after which they are locked out. Users with a
  valid access token are unaffected, and logout carries no budget. Before this,
  a Redis outage blocked login and registration but left refresh working; it no
  longer does. That trade is deliberate and the full reasoning is in
  "Rate limiting refresh" in the reference document.

  **No global limiter was introduced**, and no other route's policy changed.
  Refresh-token retention and pruning remain out of scope and unimplemented — a
  rate limit bounds the *rate*, not the total.

### Domain derivation

`domain` is derived server-side from the submitted URL using Go's `net/url`.
The remote page is never contacted during save.

Normalization is intentionally minimal:

1. Parse the URL and take `Hostname()`.
2. Lowercase the hostname.
3. Remove one leading `www.` if present.

No public-suffix or registrable-domain detection is performed.
`m.youtube.com` and `youtube.com` remain distinct.

### Collections

`internal/api/collection/` is implemented as a complete vertical slice.

| Endpoint                           | Status | Notes                                                |
| ---------------------------------- | ------ | ---------------------------------------------------- |
| `PUT /saved-items/:id/collection`  | done   | 200, owner-scoped, requires Bearer                   |
| `GET /collections`                 | done   | 200, owner-scoped, cursor-paginated, requires Bearer, **reports a bounded saved item count per collection** |
| `GET /collections/:id/saved-items` | done   | 200, owner-scoped, cursor-paginated, requires Bearer |
| `DELETE /collections/:id`          | done   | 200, owner-scoped, requires Bearer                   |

The endpoint accepts
`{"collection_name": "...", "collection_intent": "existing" | "create"}` and
files one saved item into one of the user's collections. `collection_intent` is
required. `existing` selects a destination, creating a user collection only when the
name no longer resolves; `create` asks for a new collection and refuses a name that
is already taken with 409 `COLLECTION_NAME_ALREADY_EXISTS`.

Important behavior:

- Collection creation and item movement are atomic.
- Filing an item into its current collection is idempotent.
- Unknown and foreign item IDs return the same 404 response.
- The `Unsorted` system collection is resolved by display name here, like any other
  destination; its `system_key` remains its stable identity elsewhere.
- Any existing collection owned by the user is a valid destination, including
  system collections such as `Unsorted` and platform collections such as `YouTube`.
- Collection names are unique per user across both types. Under `existing`, a name
  is resolved to that collection rather than rejected as reserved or duplicated;
  under `create`, it is a conflict.
- Collection names are trimmed and compared case-insensitively.
- A create-intent conflict moves nothing and creates nothing.
- Enrichment does not participate in collection movement.

Committed as `1e85c5d feat: add saved item collection flow`.

### Collection deletion

`DELETE /collections/:id` is implemented. It is a separate explicit operation from
deleting a saved item, and the two never invoke each other.

Request body, a stable shape in both cases:

```json
{ "saved_items_action": "delete", "target_collection_id": null }
{ "saved_items_action": "move",   "target_collection_id": "<uuid>" }
```

Behavior:

- `saved_items_action` is required and is `delete` or `move`. Nothing is defaulted,
  because the two actions have opposite consequences and one of them destroys saved
  items.
- `target_collection_id` must be null for `delete` and a collection id for `move`.
  Both fields always exist and never change meaning.
- `delete` removes the collection's saved items along with the collection. `move`
  files them into the target collection first, then removes the collection.
- The target must exist, belong to the authenticated user, and differ from the
  source. A target that does not exist is an error; there is no fallback to any
  collection, including Unsorted.
- **Unsorted is a valid move target and is named by its id** like any other
  collection. There is no `move_to_unsorted` flag and no special mode for it.
- Any collection may be deleted regardless of `type`. Unsorted is the only exception,
  identified by `system_key = 'unsorted'` and never by display name or type.
- The whole operation is one repository-owned transaction: load the source, refuse
  Unsorted, resolve the target when moving, dispose of the children, delete the
  collection, commit. Anything failing rolls all of it back.
- Nothing is taken `FOR UPDATE`, which keeps the lock order compatible with the move
  endpoint and lets the foreign key decide emptiness.
- The response is a plain `{"data": null, "message": "collection deleted
successfully"}`. The request already states the disposition, so the response adds
  nothing by listing what was done.

Error contract: `COLLECTION_NOT_FOUND`, `UNSORTED_COLLECTION_PROTECTED`,
`INVALID_COLLECTION_DELETE_ACTION`, `INVALID_COLLECTION_DELETE_TARGET`,
`COLLECTION_DELETE_TARGET_NOT_FOUND`, `COLLECTION_NOT_EMPTY`.

### Collection listing — `GET /collections`

The user's collections, one page at a time, with Unsorted pinned to the front.

Request:

```http
GET /collections?sort=newest&limit=20&cursor=<opaque>
```

| Param    | Values                     | Default  | Notes                                                                 |
| -------- | -------------------------- | -------- | --------------------------------------------------------------------- |
| `sort`   | `newest`, `oldest`, `name` | `newest` | Unknown value is a 400, never guessed                                 |
| `limit`  | 1–50                       | 20       | Above the maximum is `VALIDATION_ERROR`; `limit=0` is read as absent  |
| `cursor` | opaque token               | none     | Resume where the previous page ended; must match the requested `sort` |

Behavior:

- **Unsorted is always first, under every sort**, and appears on no later page. It
  is pinned by a rank over `system_key = 'unsorted'` in SQL, never by display name
  and never by `type`. Pinning it by position rather than filtering means all three
  sorts inherit it from one rule.
- **Collections automatic organization created are listed**, unfiltered. A
  collection belongs to one user either way.
- **`name` sorts by `lower(btrim(name))`** — the same expression
  `collections_user_name_unique` is built on, so the ordering and the uniqueness
  constraint cannot disagree about what two names being the same means.
- **Every order is broken by `id`.** `created_at` defaults to `NOW()`, which is
  constant within a transaction, so equal timestamps are real: migration 000022
  inserts every user's Unsorted collection in one statement. Without a unique
  tie-break a page boundary can skip or repeat a row.
- **Nine named queries**, three sorts times three resume points. The "six" in older
  revisions of this sentence was wrong; the count is corrected here. sqlc derives
  parameter types per statement, so a runtime-selected `ORDER BY` would either
  collapse the types or force a query framework this feature has no use for.
- **Every statement also projects `saved_items_count` and
  `saved_items_count_overflow`**, through a `LEFT JOIN LATERAL` correlated on the
  collection's own id and owner. One statement, one plan, one round trip, so naming
  what a collection holds costs nothing beyond the read already happening. Added
  2026-10-18; see decision 19j.
- **The resumed queries exclude Unsorted with `IS DISTINCT FROM`.** That is
  load-bearing twice: it removes the pinned row, and it is NULL-safe, so user
  collections whose `system_key` is NULL are not dropped with it. A plain `<>` would
  return system collections only.
- **Row comparison in the keyset predicate, with explicit casts.** The casts are
  load-bearing too: without them sqlc types every row-comparison parameter from the
  row's leftmost element, which would give the `id` parameter a `timestamptz` type.

Response:

```json
{
  "data": { "collections": [{ "id": "…", "name": "Unsorted", "type": "system", "system_key": "unsorted", "saved_items_count": 4, "saved_items_count_overflow": false, "created_at": "…", "updated_at": "…" }] },
  "message": "collections retrieved successfully",
  "meta": { "cursor": { "next_cursor": null, "has_more": false } }
}
```

- `system_key` is included so a client recognizes Unsorted by identity rather than by
  display name, and can know a collection may not be deleted before offering to
  delete it.
- **`saved_items_count` is bounded at 99 and `saved_items_count_overflow` says
  whether the real count was higher.** Together they carry 0, 1–99 and 100+, so a
  client renders an exact badge or a `99+` without comparing against a threshold of
  its own. It counts the **whole collection** on **every page**, and an empty
  collection reports `0` rather than null. Both are added 2026-10-18; see decision
  19j and the change section above.
- **`next_cursor` has no `omitempty`**, so it serializes as an explicit `null` on the
  final page. A nil pointer tagged `omitempty` would drop the key entirely, and a
  missing key and a null are two different things to a client decoding it.
- **No `total` or `total_pages`.** `has_more` comes from one extra fetched row rather
  than a count, which is the work keyset pagination exists to avoid. That is about
  the size of the **page**; `saved_items_count` is about the size of a collection and
  the two do not collide.
- An empty page serializes `collections` as `[]`, never `null`.
- **`meta` is declared on the API response type as a documentation-only mirror.**
  `httpx.OKWithMeta` writes the real envelope, so a struct that omits `meta` returned a
  three-key body while the generated schema described two — and the `@Description`
  above, which talks about `meta.cursor.next_cursor`, then contradicted the schema
  under it. Still valid OpenAPI, so no linter caught it. Fixed here and on
  `GET /collections/:id/saved-items`; both now share one `CursorPageMeta`. See the
  "Documented response shape" test below.

### A collection's saved items — `GET /collections/:id/saved-items`

Where the saved items of one collection are seen. It replaces the removed standalone
listing, because every saved item belongs to exactly one collection.

Request:

```http
GET /collections/:id/saved-items?limit=20&cursor=<opaque>
```

Response:

```json
{
  "data": {
    "collection": { "id": "01a0f359-093b-737a-963a-80f7ca6768ed", "name": "YouTube" },
    "saved_items": [
      {
        "id": "01a0f359-093b-737a-963a-80f7ca6768ed",
        "url": "https://example.com/video",
        "domain": "example.com",
        "platform": null,
        "title": null,
        "description": null,
        "image_url": null,
        "collection_id": "01a0f359-093b-737a-963a-80f7ca6768ed",
        "enrichment_status": "pending",
        "last_enriched_at": null,
        "created_at": "2026-10-09T00:00:00Z",
        "updated_at": "2026-10-09T00:00:00Z"
      }
    ]
  },
  "message": "saved items retrieved successfully",
  "meta": {
    "cursor": { "next_cursor": null, "has_more": false }
  }
}
```

Behavior:

- **The collection is resolved and its ownership proved before any item is read.** A
  collection that does not exist and one owned by somebody else produce the same
  `COLLECTION_NOT_FOUND`, so the endpoint never discloses whether an id exists.
- **`data.collection` carries `id` and `name`, and nothing else.** The caller arrived
  knowing the id; the name is the one thing the path cannot tell it. It is reused from
  the ownership lookup rather than read again — no second query was added — and it is
  reported on every response, **including an empty page**, because an empty collection
  is still a named collection and omitting the field would make "nothing in it"
  indistinguishable from "no match". `type` and `system_key` are deliberately absent:
  they answer questions for `GET /collections`, not this one.
- **Unsorted is an ordinary collection here.** Nothing is special-cased for it: the
  inbox is this endpoint pointed at the Unsorted id, and it arrives through the same
  `collection` object as anything else.
- **Each item is reported in full**: `id`, `url`, `domain`, `platform`, `title`,
  `description`, `image_url`, `collection_id`, `enrichment_status`,
  `last_enriched_at`, `created_at`, `updated_at`. The previous standalone listing
  reported seven of these, so a caller could not tell a missing title from metadata
  that had not arrived.
- **`collection_id` stays on every item** and always equals `data.collection.id`. The
  redundancy is deliberate: it makes an item self-describing once it is carried out of
  this response.
- **A null title, a null description, a null image url and a `failed` enrichment are
  all ordinary states and all listed.** `enrichment_status` is `NOT NULL` with a
  default; the metadata columns are nullable and stay so after a page exposes none.
- **Ordering is fixed at `created_at DESC, id DESC`.** No sort parameter. A collection
  is something a person curated, so the order they chose when filing is the one they
  see.
- **`limit` defaults to 20 and is capped at 50.** A limit of 51+ or below 1 is
  `VALIDATION_ERROR`; a non-numeric limit is `BAD_REQUEST`; `limit=0` is treated as
  absent. The `max=50` tag fires before the service's own clamp, so an out-of-range
  limit is rejected rather than silently resized.
- **Listing never triggers enrichment and never writes.** Enrichment is scheduled by
  saving and by `POST /saved-items/:id/enrich`; browsing is not a reason to fetch.
- **Cursor-paginated**, with the same `meta.cursor` shape as `GET /collections` and an
  explicit `next_cursor: null` on the final page. A cursor is a position, so it
  survives its anchor item being deleted; a cursor past the end returns an empty page.
- **Two SQL statements, not six.** One ordering and no pinned row means one
  first-page statement and one after-position statement.
- **Its cursor payload has no `Sort` and no `Group`.** Both exist on the collection
  payload only because that listing pins Unsorted and offers three orders; fields that
  can never vary here would only add unreachable validation branches. There is a test
  asserting the payload carries neither.
- **The timestamp is parsed and validated in the service**, so a token that decodes but
  holds an unusable value is `INVALID_CURSOR` rather than a server fault, and no query
  runs to discover it.

Errors: `400 BAD_REQUEST` / `400 VALIDATION_ERROR` / `400 INVALID_CURSOR`,
`401 INVALID_AUTHORIZATION_HEADER` / `INVALID_ACCESS_TOKEN` / `ACCESS_TOKEN_EXPIRED`,
`404 COLLECTION_NOT_FOUND`, `500 INTERNAL_SERVER_ERROR`.

### Saved item deletion reports collection state

`DELETE /saved-items/:id` deletes the saved item and **never** touches its
collection. The response reports what the delete left behind:

```json
{
  "data": {
    "collection_id": "01a0f359-093b-737a-963a-80f7ca6768ed",
    "collection_empty": true,
    "collection_deletable": true
  },
  "message": "saved item deleted successfully"
}
```

- `collection_id` comes from the DELETE's own `RETURNING` clause, so it is the
  deleted row's value and a concurrent move cannot make it disagree. No locking
  read is needed to pin it.
- `collection_empty` is counted after the delete, scoped by `collection_id AND
user_id`.
- `collection_deletable` is `collection_empty` and the collection not being
  Unsorted. It exists because Unsorted is where every save lands, so an empty inbox
  is the most common "yes" here and a caller acting on `collection_empty` alone
  would be turned away on almost every inbox deletion.
- All three are advisory. The collection is untouched, and deleting it is a separate
  request that validates the current state again.
- There is deliberately no transaction around the delete and its two reads: nothing
  is acted on, and under READ COMMITTED each statement snapshots separately anyway.
- The previously `null` data body is replaced, which is a deliberate API change.

### Search

`internal/api/search/` is implemented as a complete vertical slice.

| Endpoint      | Status | Notes                              |
| ------------- | ------ | ---------------------------------- |
| `GET /search` | done   | 200, owner-scoped, requires Bearer |

Search is PostgreSQL-native and uses `pg_trgm` with `word_similarity()` as
the fuzzy fallback. No external search engine or PostgreSQL full-text search
is used for v1.

Search behavior:

- `q` is trimmed and must be 2–128 runes.
- Results are always arrays; no-match is a successful 200.
- Saved items match on `title`, `domain`, and `url`.
- Collections match on `name`.
- Results are user-scoped.
- Scores are internal and are not part of the public API contract.
- A saved item result reports `id`, `title`, `url`, `domain`, `image_url`,
  `enrichment_status`, a nested `collection` of `id` and `name`, and `created_at`.
  The collection is joined in by the search query itself, so naming it costs no
  extra query per result.
- A collection result reports `id` and `name` only; `type` and `system_key` are
  reported by `GET /collections` rather than by a search result.
- Saved items default to 20 results and cap at 50.
- Collections cap at 5.
- Cursor pagination, autocomplete, and suggestions are not implemented.

#### Search is now bounded per authenticated user — implemented

`GET /search` carries a per-user budget: **30 requests per minute**, namespace
`search-user`, mounted as `middleware.UserRateLimit` after `Auth` and before the
handler. `search.RegisterModule` takes the existing `pinRateLimiter` instance, so
there is no second Redis connection, secret or limiter.

The reason is that search is the only route whose cost is proportional to
something the caller owns rather than being a fixed amount of work. Both search
statements are user-scoped, so no cross-user scan is possible — but the plan
examines **all** of that account's rows. The predicate is three `ILIKE '%q%'`
clauses and three `word_similarity()` calls per row, and the *function* form of
`word_similarity` cannot be answered from the trigram index, so the index changes
the access path without reducing rows read. `migrations/000023` already records
this: domain and url are "simply resolved by a sequential scan on the user-scoped
subset".

**What was measured, and what it is not.** The cost profile was established
against a **disposable PostgreSQL container** running this project's baseline
schema, with synthetic accounts at 100 / 1,000 / 10,000 / 50,000 saved items and
`EXPLAIN (ANALYZE, BUFFERS)` on the exact production SQL:

| Saved items | Plan | Rows examined | Buffers | Execution time |
| ----------- | ---- | ------------- | ------- | -------------- |
| 100 | Index Scan | 100 | 19 hit | 1.8 ms |
| 1,000 | Index Scan | 1,000 | 65 hit | 17.9 ms |
| 10,000 | Bitmap Heap Scan | 10,000 | 471 hit | 184 ms |
| 50,000 | Parallel Seq Scan | 50,000 | 2,821 hit | 483 ms |

A query matching **nothing** at 50,000 items cost 452 ms — the scan does not get
cheaper for a selective query, because selectivity cannot be used. All buffers
were `shared hit`, so this is CPU rather than I/O. The sort is not a factor
(top-N heapsort, 44 kB). `SearchCollections` measured 0.094 ms and needs nothing.

**That benchmark does not guarantee production latency.** It ran on an idle
developer machine, not production hardware under concurrent load, against
synthetic data, and the pool is unconfigured (`max(4, NumCPU)` connections), so
absolute numbers will differ. What it establishes is the *shape* — cost linear in
the account's own rows — not an optimal rate.

**Thirty a minute is a provisional threshold, not a measured optimum.** It is
chosen to sit comfortably above interactive use (a search box is person-driven,
so even deliberate exploration is a handful of requests a minute), at three times
the enrichment budget because the two routes have opposite interactive
characters. The one-minute window matches the enrichment convention and means a
burst costs a user a minute of degraded search rather than ten. Reassess against
real traffic.

**Follow-up not done here:** making the query cheap rather than rationing it.
Indexing `domain` and `url` with `pg_trgm`, or moving the fuzzy predicate to the
index-accelerated `<%` operator, would reduce the scan itself and is the better
fix on the merits. It is a schema and query change, was explicitly out of scope
for this task, and would not have prevented the connection-exhaustion shape in the
meantime — which is why the limiter landed first.

Search is complete and committed.

### SQLC generated-model cleanup

`omit_unused_structs: true` is enabled across all SQLC targets.

Each target now generates only the models required by its own queries.
Generated query code was not changed by the cleanup.

Saved Item owns its projected `saved_item.SavedItem` type rather than borrowing
an unrelated generated table model. Cross-feature models with real consumers,
such as `sessiondb.Session`, remain generated.

`AGENTS.md` documents the code-generation rule.

---

## Enrichment — foundation and both application paths complete

The enrichment foundation and both application integrations are complete. The
background worker and automatic organization have their own section below.

The current system has these relevant layers:

1. `internal/security` — guarded outbound HTTP and SSRF protection.
2. `internal/enrichment` — reusable metadata fetching and extraction, shared by
   every caller.
3. `internal/api/enrichment` — synchronous Saved Item application integration.
4. `internal/worker/enrichment` — background enrichment worker, driven by an Asynq
   task.
5. `internal/worker/organization` — background automatic organization, driven by an
   Asynq task the enrichment worker produces.

Enrichment runs either on request for one saved item or in the background after a
save. The two paths write the same rows.

### Enrichment schema

The current `saved_items` schema contains:

| Column              | Meaning                                           |
| ------------------- | ------------------------------------------------- |
| `title`             | optional metadata-derived title                   |
| `platform`          | optional metadata-derived content/source platform |
| `description`       | optional enriched description                     |
| `image_url`         | optional enriched preview image                   |
| `enrichment_status` | `pending`, `completed`, or `failed`               |
| `last_enriched_at`  | time of the last successful enrichment            |

`pending` means enrichment has not successfully completed yet.

`completed` means the enrichment process succeeded. It does **not** mean every
metadata field was found. A page may produce only a title, only an image, or no
usable optional metadata and still be `completed`.

`failed` means the enrichment process itself failed.

Enrichment failure never invalidates the Saved Item. The URL remains the actual
saved data regardless of enrichment status.

### Outbound fetch security

`internal/security/outbound_fetch.go` is the SSRF boundary for server-side
fetching of user-supplied URLs.

The guarded HTTP client:

- allows only `http` and `https`;
- allows only ports 80 and 443;
- validates resolved addresses at dial time;
- blocks loopback, private, link-local, multicast, unspecified, unique-local,
  metadata, and other special-purpose addresses;
- protects against DNS rebinding;
- limits redirects, timeouts, and response size;
- does not use an environment proxy;
- rejects local-only hostnames and unsafe URL forms early.

No second outbound client or duplicate SSRF implementation should be added.

### Metadata extraction

`internal/enrichment/` is independent of Fiber, SQLC, persistence, and Asynq.

It extracts metadata such as:

- title
- description
- image URL
- canonical URL
- platform
- author/site name when available

Supported sources include HTML title/meta tags, Open Graph, Twitter metadata,
canonical links, and JSON-LD.

Metadata is optional. Missing metadata is not an enrichment failure.

`platform` is derived only from metadata published by the page. It is never
inferred from the hostname or from the client's `X-Platform` header.

Relative metadata URLs are resolved against the final page URL. The extractor
does not perform additional network requests for metadata URLs.

`CanonicalURL`, `Author`, and `SiteName` are currently extraction outputs only;
they are not persisted to `saved_items`.

### Synchronous per-item endpoint

`POST /saved-items/:id/enrich` is implemented and manually verified.

Behavior:

- authenticated and owner-scoped;
- operates on exactly one Saved Item;
- resolves ownership before performing any outbound request;
- uses the existing guarded HTTP client and enrichment extractor;
- can be called repeatedly regardless of the current enrichment status;
- never modifies `url`, `domain`, or `collection_id`;
- never performs automatic collection organization.

On successful enrichment:

- `title`, `platform`, `description`, and `image_url` are written from the
  current enrichment result;
- missing fields are written as `NULL`, so stale metadata is not retained;
- `enrichment_status` becomes `completed`;
- `last_enriched_at` is updated.

On enrichment failure:

- `enrichment_status` becomes `failed`;
- previously stored metadata is preserved;
- `last_enriched_at` is unchanged.

The endpoint returns 200 for both completed and failed enrichment outcomes.
The outcome is represented by `enrichment_status`; internal failure details
are logged rather than exposed through the normal API response.

### Verification

The feature has:

- 8 service unit tests;
- 15 integration tests;
- 90.5% combined feature-package coverage;
- 95.4% statement coverage in `internal/enrichment`;
- 100% statement coverage for `internal/security/outbound_fetch.go`.

Manual Postman verification was performed against real external URLs:

| Case                               | Result                             |
| ---------------------------------- | ---------------------------------- |
| YouTube                            | `completed`, rich metadata         |
| Spotify                            | `completed`, platform and metadata |
| Aura                               | `completed`, partial metadata      |
| `127.0.0.1`                        | `failed`, blocked by SSRF policy   |
| `169.254.169.254`                  | `failed`, blocked by SSRF policy   |
| Public host with a non-80/443 port | `failed`, blocked by port policy   |

The real extractor has therefore been verified end-to-end in addition to the
automated tests.

---

## Background enrichment worker — implemented

Background Asynq enrichment is implemented and verified. The worker enriches a
Saved Item after it has been committed, and the synchronous endpoint remains the
per-item on-demand path.

### What exists

Infrastructure:

- local Redis service in `docker-compose.dev.yml`;
- `go-redis/v9` and `hibiken/asynq`;
- separate `WorkerConfig` / `LoadWorker` — the worker reads only `DATABASE_URL`,
  `REDIS_URL`, `APP_ENV`, and the optional `WORKER_CONCURRENCY`, which is range
  checked when it is set;
- `cmd/worker` binary with a Redis connectivity check and graceful signal
  blocking;
- `REDIS_URL` is now required by the API as well as the worker, since a task can
  only be processed by a worker pointed at the same Redis.

Packages:

- `internal/worker/queue` — the queue contract. Holds `TaskTypeEnrichSavedItem`,
  `QueueEnrichment`, the task timeout and retry budget, the payload type, the
  retry delay function, and the `Producer` the API enqueues through. The package
  imports no Fiber, nothing from `internal/api`, and no persistence.
- `internal/worker/enrichment` — the worker's own handler, service, repository and
  narrow projections, plus its own sqlc target.
- `internal/api/saved_item/enqueuer.go` — the one-method interface the save flow
  depends on. It may be nil, which is how "this process schedules nothing" is
  expressed.

`cmd/worker` imports nothing from `internal/api`.

### Save → enqueue flow

1. The row is written. `domain` is derived locally and nothing contacts the
   remote source.
2. Only once that write has committed is enrichment queued, with the saved item's
   id as the whole payload.

Enqueueing before the write returned could put a task in Redis for an id the
database never accepted. Enqueueing after it makes the worst case the opposite
and smaller: a committed row whose task was never queued, which is a valid Saved
Item that still says `pending` and can be enriched on demand.

A failure to enqueue is logged and swallowed. The save succeeded, and reporting
it as a failed request would report a failure for something that happened.

### Task and consumer

The payload carries the saved item's id and nothing else. The worker reads the
URL from the row, so a queued copy can never disagree with what was stored.

The worker reuses `internal/enrichment` and `internal/security`, constructing the
same guarded outbound client the API uses. There is no second SSRF
implementation and no second outbound client.

### Queue and task naming

Both areas name their task type `<area>:<thing>` after their queue, so a task and
the queue it belongs to can be read off one string:

| Queue          | Task type                 |
| -------------- | ------------------------- |
| `enrichment`   | `enrichment:saved_item`   |
| `organization` | `organization:saved_item` |

Both are dedicated queues rather than Asynq's `default`, and both are declared
explicitly in the worker's `Queues` map. Organization is not on the enrichment
queue because its work is pure database: a burst of saves would otherwise delay
filing behind its own page fetches.

### Persistence semantics

Identical to the synchronous endpoint, statement for statement:

| Outcome              | `enrichment_status` | Metadata  | `last_enriched_at` |
| -------------------- | ------------------- | --------- | ------------------ |
| extraction succeeded | `completed`         | written   | updated            |
| extraction failed    | `failed`            | preserved | unchanged          |
| page exposed nothing | `completed`         | all NULL  | updated            |

`collection_id` and `domain` are never written by enrichment. There is no
`processing` or `retrying` value and no intermediate state: how many more attempts
the queue will make is execution mechanics, not the state of the Saved Item.

A task for a deleted item is discarded rather than retried, and no outbound
request is made for a row that is gone.

### Retry classification

The classification is read from `enrichment.Kind`, so the worker does not define a
second opinion about what a page error means. It maps that classification onto one
queue decision.

Retryable:

- DNS, connection, TLS, timeouts, too many redirects, policy refusals;
- a body that could not be read;
- `429` and `5xx`, which describe the origin at that moment;
- any database error, which arrives unclassified and defaults to retryable.

Permanent, and skipped immediately:

- a terminal client status such as `401`, `403`, `404`, `410`;
- a non-HTML content type;
- an HTML document that could not be parsed.

Retries are bounded by the queue's own budget: one attempt plus
`EnrichmentMaxRetry` retries, with an exponentially increasing, jittered, capped
delay. Once the budget is exhausted Asynq archives the task and the item already
carries the durable `failed` record.

### Concurrency and shutdown

`WORKER_CONCURRENCY` remains the runtime configuration source, with an explicit
range enforced by the loader:

| Setting                    | Value |
| -------------------------- | ----- |
| `DefaultWorkerConcurrency` | `5`   |
| `MinWorkerConcurrency`     | `1`   |
| `MaxWorkerConcurrency`     | `20`  |

An unset value resolves to the default. A value that is present but outside the
range — zero, negative, above the maximum, or not a number — fails configuration
and stops the worker from starting, rather than being silently replaced. A
deployment that believes it asked for something it did not get is worse than one
that refuses to start.

Enrichment is network-bound, so a small number of concurrent attempts overlaps
mostly waiting, while a large number only multiplies concurrent outbound requests
and database connections against origins that are already slow.

On SIGTERM the worker stops taking new tasks and waits for in-flight ones, bounded
by `shutdownTimeout`. Anything still queued stays in Redis for whichever worker
runs next.

The startup log records the Redis address and database, never the full URL, which
may carry credentials.

### Automatic organization

Background organization is implemented. It is a separate task type on its own
queue, produced by the enrichment worker and consumed by the worker binary.

Flow, with no scan or sweep anywhere in it:

```
save -> enrichment task -> enrichment worker
     -> completed enrichment with a platform
     -> one organization task (saved_item_id + user_id)
     -> organization worker
     -> re-read the item, locked
     -> file it only if it is STILL in Unsorted
```

A failed enrichment records `failed` and queues nothing. A completed enrichment
with no platform queues nothing. There is no persisted organization state, so a
task that was never created is never created later: an item left in `Unsorted`
stays there until a user asks for it to be enriched again.

Package `internal/worker/organization` owns its own handler, service, repository,
projection and sqlc target, mirroring the enrichment worker. It imports nothing
from `internal/api` and nothing from the enrichment worker.

The worker decides at execution time, against a locked row:

| Condition                                 | Outcome                               |
| ----------------------------------------- | ------------------------------------- |
| item does not exist                       | discarded, like the enrichment worker |
| item belongs to another user              | refused and archived                  |
| `enrichment_status` is not `completed`    | no-op                                 |
| `platform` is NULL                        | no-op                                 |
| item is no longer in `Unsorted`           | no-op, never moved back               |
| a collection already matches the platform | reused, never renamed                 |
| no collection matches                     | created as `type = 'system'`          |

The "still in Unsorted" rule is the one that matters: an item the user filed into
their own collection while the task waited is left exactly where they put it.

Ownership is enforced by migration 000025's composite foreign key, and the worker's
statements are scoped to agree with it. A created collection belongs to that one
user; `system` says who created it, not who owns it.

`system_key` is the platform's own normalized identity — `lower(btrim(platform))` —
so it agrees with the `collections_user_name_unique` comparison and the two indexes
can never disagree. It is a derived identity, not a curated registry, which is the
honest consequence of `platform` being free text published by the page.

### Still not implemented

- Rate limiting for endpoints other than the PIN endpoints, verification and
  manual finalization, manual login and the Google authentication routes. Every
  registration route that sends a message or does expensive work is now
  limited: see "Registration rate limiting" and "Registration verification and
  finalization" below. Manual login has two budgets and Google authentication
  has one. Password reset is limited on all four of its working routes, and the
  two saved-item routes that cost something beyond a read are limited per user:
  see "Per-user budgets on the saved-item routes" below. **Collections, search,
  and the reads and deletes on saved items remain deliberately unlimited.** The
  collection and search routes spend no shared resource beyond an ordinary query
  and hold no cursor or lock that outlives the request.
  `GetVerification` and `GetRegistrationContinuation` are deliberately unlimited
  as well: both are pure reads, and one of them is what a client polls to learn
  when it may resend.
- A possible one-off backfill of items enriched before automatic organization
  existed, which are still in `Unsorted` with a platform. This is an open product
  decision rather than a gap in the mechanism, and it would be a separate
  operation — see decision 13a for why it is not part of the normal worker flow.
- Cloud Run deployment specifics. The worker is a standalone binary with no HTTP
  surface, and is intended to be deployable as-is.

What is deliberately **not** implemented, and should not be read as missing:

- **No organization reconciliation, scanner or sweep.** Organization is event-driven
  from a successful enrichment that produced a platform, and there is no
  alternative producer. With no persisted organization status there is nothing to
  sweep _for_, and a query for "still in `Unsorted` with a platform" would also
  match exactly the state a user may have chosen on purpose. If an event is never
  produced, the system does not later discover it — deliberately, not by accident.
- **No reconciliation for items left `pending` by a failed enqueue.** Also a
  deliberate trade-off, recorded as decision 16.

### Verification

The worker has:

- `internal/config` — the concurrency policy: the default, both range boundaries,
  accepted values inside the range, and rejection of zero, negative,
  above-maximum and non-numeric values, all through `LoadWorker`;
- `internal/worker/enrichment` — the enrichment -> organization handoff: a completed
  enrichment with a platform schedules one task, and a failed extraction, a
  complete extraction with no platform, a failed write and an unreachable queue do
  not;
- `internal/worker/organization` — the eligibility guards, the system key derived
  from a platform, and that organization writes no enrichment state;
- `internal/worker/queue` — payload round trip, payload contents, backoff bounds,
  and the relationship between the task timeout and the outbound fetch ceiling;
- `internal/worker/enrichment` — handler classification for retryable, permanent,
  unclassified and malformed-payload tasks, and service behavior for completion,
  empty results, failures, missing items and write errors;
- `internal/integration` — the whole path against real Postgres and real Redis:
  a save enqueues, the worker consumes, the row is written; a deleted item is
  discarded; a permanent failure is archived on the first attempt; a transient
  failure is retried and then completes; an exhausted budget is archived; the
  producer's task type, queue, timeout and retry budget round-trip for both task
  types; the enrichment -> organization handoff runs end to end; and organization
  creates, reuses, is skipped for, and refuses to undo a user's filing for, with
  concurrent tasks producing exactly one collection per platform per user.

Deletion is verified against real Postgres, asserting database state rather than
status codes alone:

- `internal/api/saved_item` — the delete's reported collection, count scoping, the
  empty and not-empty cases, Unsorted against a system collection and against a user
  collection with no key, not-found leaving the collection unread, and error
  propagation from all three repository calls.
- `internal/integration` — reported collection state for user, system and Unsorted
  collections; the reported id being the deleted item's own after a real move;
  concurrent deletes of the last two items and of the same item, asserting invariants
  rather than an interleaving.
- `internal/api/collection` — the request rules, both dispositions, Unsorted as an
  ordinary target by id, and error pass-through.
- `internal/integration` — both dispositions against real rows; Unsorted refused
  while another system collection of the same user is deleted; the foreign key
  proven to be the guard, asserted by `ConstraintName`; ownership isolation and
  byte-identical not-found responses; and concurrency asserting that no saved item
  ever references a missing collection.

Queue-facing tests read Asynq's own accounting through an `Inspector` rather than
by counting Redis keys, so they cannot pass by reading nothing.

---

## Registration verification and finalization — hardened

Two changes to the middle and end of the registration flow: the two
single-use transitions are now covered by concurrency tests against real
PostgreSQL, and the two routes that were unbounded now carry their own
per-client-address budgets.

### Concurrency coverage for the single-use transitions

`VerifyRegistration` and `FinalizeManualRegistration` are single-use by
design, and both validate **outside** their transaction and mutate **inside**
it. Two requests carrying the same credential can therefore both pass every
check the service makes before either reaches the database. What separates
them is the database, and until now nothing proved it.

Two integration tests now do, in
`internal/integration/auth_register_concurrency_test.go`:

| Race                               | Asserted                                                                                                                                                       |
| ---------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Two `verify` calls, same valid PIN | exactly one 200, one 409; **one** continuation; **one** consumed code; `verification_requests.status = 'verified'`; registration still `pending`               |
| Two `finalize` calls, same token   | exactly one 201, one 409 `USER_ALREADY_EXISTS`; **one** user, **one** credential, **one** Unsorted collection; continuation consumed; registration `completed` |

Both use the suite's existing `runConcurrently` barrier — every goroutine is
built and parked before any is released — and every assertion is read from
the database rather than inferred from a status code. No sleep is used as
synchronization anywhere in the file.

**What these tests do and do not prove, measured rather than assumed.** Each
guard was removed in turn and the suite re-run:

- `ConsumeVerificationCode`'s `consumed_at IS NULL` predicate,
- `MarkVerificationRequestVerified`'s `status = 'pending'` predicate,
- the unique index on `registration_continuations(pending_registration_id)`,

and the verification test passed with each one gone. It holds the invariant
of "exactly one continuation", but it does not discriminate _which_ guard
produces it, because in a losing request the rejection usually happens at
the service's first read — `GetVerification` already returns
`VERIFICATION_NOT_PENDING` — before the transaction is ever opened. The
guards are therefore redundant to one another, which is the correct
arrangement for a security-relevant transition, and the continuation count
remains the assertion that matters. What a guard removal would break is a
request that reaches the transaction without having observed the winner, a
narrower window than a barrier against two HTTP requests can open.

The finalization test is sharper: its loser provably loses the unique index
on `users.email`, which the request log shows directly
(`duplicate key value violates unique constraint "users_email_unique"`).

### Two new per-client-address budgets

`VerifyRegistration` and `FinalizeManualRegistration` previously had no
rate limit at all. Each now carries its own, under its own namespace:

| Budget | Route                                         | Limit             | Namespace     |
| ------ | --------------------------------------------- | ----------------- | ------------- |
| per-IP | `POST /auth/register/verification/:id/verify` | 20 per 10 minutes | `verify-ip`   |
| per-IP | `POST /auth/register/finalize/manual`         | 10 per 15 minutes | `finalize-ip` |

Both are mounted **in front of the handler**, for the reason every other
budget in this repository is: a verification id that is not a UUID, or a
finalization carrying no token at all, never becomes a request the handler
sees, and those are the cheapest requests to send. Both inherit the shared
middleware's fail-closed behaviour and `Retry-After` semantics unchanged —
`CLIENT_IP_SOURCE`, trusted-proxy handling and client-IP validation are
untouched.

**Neither budget is about guessing.** The per-code attempt limit already caps
PIN guesses at five, and it is enforced by the database rather than by
anything here. `verify-ip` bounds the cost of a wrong attempt: a verification
lookup, a code lookup, an HMAC comparison and a write to the attempt counter,
none of which was bounded anywhere. `finalize-ip` bounds the only route in
the flow that performs an **Argon2id derivation**. The shared
`PasswordWorkLimiter` bounds how many derivations run at once across the
process, but that is a capacity limit, not a rate limit: it stops the process
being overrun without stopping one caller from occupying every slot
indefinitely.

**Each namespace is deliberately separate from every other flow's.** Verify
and finalize are adjacent steps of one registration and are two different
acts, so exhausting one must not spend the other — a user who mistypes a PIN
has to be able to finalize once they get it right. Both are separate from
`pin-ip`, because verification does not send a message and must not spend
the allowance that governs mailing a new code. Both are separate from the
login budgets, because reaching a verification route is not an
authentication attempt and an exhausted registration budget must not lock
anyone out of signing in with a password they already have.

**No per-email budget was added, deliberately.** The email is not known
before the expensive work: `VerifyRegistration` learns it only by resolving
the verification, and finalization reads it from the continuation row. A
per-email key here would be an attacker-chosen value, letting an attacker
lock a legitimate registrant out of their own verification — the same
lockout reasoning that keeps `google-auth-ip` free of a per-email dimension.

`GetVerification` and `GetRegistrationContinuation` remain **deliberately
unlimited**. Both are pure reads; limiting them would cost a polling client
its budget without bounding any expensive work, and `GetVerification` is
exactly what a client calls to learn when it may resend.

**The thresholds are initial engineering estimates, not production-validated
values.** No deployment has been selected and no traffic observed. Revisit
once they have run against real traffic and real shared addresses.

### `registration_continuations.token_hash` is now indexed

Migration `000027_add_registration_continuations_token_hash_unique_index`
adds a unique index on `registration_continuations(token_hash)`, and
`internal/database/baseline/schema.sql` now reflects it.

The lookup column was previously unindexed while the sibling
`password_reset_continuations` had carried `password_reset_continuations_token_hash_idx`
since migration 000014. Every continuation read — `GetRegistrationContinuation`,
and therefore both the continuation route and both finalization routes —
was a sequential scan on a table that grows with every registration, reached
from unauthenticated endpoints. The baseline is updated so integration tests
exercise the same index production does.

### Verification

`internal/integration` — both concurrency races against real PostgreSQL, with
database-state assertions; the two new budgets against real Redis through
HTTP; each budget spent and refusing with a `Retry-After` derived from the
counter and never extending the window; one address's flood not affecting a
neighbour; verify and finalize proven separate in **both** directions; the
new namespaces proven not to touch `pin-ip`, `pin-email`, `login-ip`,
`login-email` or `google-auth-ip`, asserted from the limiter's own
accounting rather than from response codes; the unlimited read routes proven
to consult no limiter at all; an unusable limiter refusing both new routes
with 503 and no `Retry-After`; and a malformed verification id proven to be
counted in full while still being answered 400 by the handler.

**The namespace tests were proven to detect what they claim.** With
`finalize-ip` temporarily pointed at `pin-ip`, `NewNamespacesDoNotSpendExistingBudgets`
fails and passes again once restored.

### Deployment considerations

- Both new budgets fail **closed**. A Redis outage stops verification and
  finalization outright, which is intended, but it is a new way for an
  outage to be visible in registration. The registration flow already
  depends on Redis for PIN issuance; the dependency is now total rather than
  partial.
- All five thresholds — `pin-email`, `pin-ip`, `verify-ip`, `finalize-ip` —
  remain unvalidated against production traffic. A shared address behind
  CGNAT is charged all of them, and `finalize-ip` is the tightest.
- The race detector could not be run in the environment this work was done
  in (`CGO_ENABLED=0`, no C compiler). The concurrency tests are proven
  correct against real PostgreSQL but have not been run under `-race`.

---

## Social registration finalization — concurrency coverage

`FinalizeSocialRegistration` is single-use in exactly the way
`FinalizeManualRegistration` is, and it had no concurrency coverage at all:
the social transition was the only one in the flow with a guard that had never
been exercised against two requests.

`internal/integration/auth_register_concurrency_test.go` now carries
`TestFinalizeSocialRegistration_ConcurrentSameTokenCreatesExactlyOneAccount`.
It is a test-only change; no production code was touched.

### The flow is driven end to end

The test does not seed a continuation. It runs the real flow through every
endpoint a client would use — `POST /auth/login/google`, then
`POST /register/verification/:id/pin`, then
`POST /register/verification/:id/verify` — and uses the token that verification
actually issued. The PIN is read out of the mail that was sent, because it is six
random digits and no value may be assumed.

The point is that the winner and the loser contend for state the production path
produced. A seeded token would prove the same database invariant, but not that the
continuation the real flow mints is the single-use one the code believes it is.

### What the losing request is told

**The observed result is `USER_ALREADY_EXISTS`**, and the request log shows the
mechanism directly:

```text
POST /auth/register/finalize/social 409 USER_ALREADY_EXISTS
ERROR: duplicate key value violates unique constraint "users_email_unique" (SQLSTATE 23505)
```

The loser opened its transaction, lost the unique index on `users.email`, and the
violation was mapped by `ConstraintName`. Its whole transaction rolled back,
including the `users` row it had already inserted.

A second documented outcome exists and the test accepts it without asserting
which one occurred: a request whose `GetRegistrationContinuation` runs **after**
the winner commits reads `consumed_at` set and is refused
`REGISTRATION_CONTINUATION_CONSUMED` at the service's validation, before any
transaction is opened. Pinning either code exactly would assert on scheduling
rather than on behaviour, so both are accepted and the invariant is asserted from
the database — the same reasoning the verification race already uses.

### Database invariants asserted

Every count is scoped to the address or the provider subject under test, never to
a global total:

| Assertion                                       | Scope                                                                                          |
| ----------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| exactly one `users` row                         | `WHERE email = $1`                                                                             |
| exactly one `auth_identities` row               | `WHERE provider = $1 AND provider_subject = $2`                                                |
| exactly one Unsorted collection                 | `c.system_key = 'unsorted'`, **not** name or `type`                                            |
| exactly one session                             | for the resulting user id                                                                      |
| zero password credentials                       | a social registration creates none                                                             |
| exactly one `pending_social_identities`         | asserted **before** the race, so a duplicate cannot be confused with one the setup left behind |
| continuation consumed, registration `completed` | `GetFinalizedSocialRegistrationState`                                                          |

`auth_identities` is counted by provider and subject rather than by user, because
that pair is what it is unique on — an identity duplicated onto a second account
would not be visible from any single user's rows.

The zero-credential assertion is what separates "the loser rolled back" from "the
loser committed an account with no way to sign in".

### What this does and does not prove

It holds "exactly one account, exactly one identity, exactly one session". Like the
two races already in that file, **it does not discriminate which guard produced
the outcome** — in a losing request the rejection often happens at the service's
first read, before a transaction is ever opened, because the winner has already
committed. The guards are redundant to one another, which is the correct
arrangement for a security-relevant transition. Removing one would break only a
request that reached the transaction without having observed the winner, a narrower
window than a barrier against two HTTP requests can open.

It does not cover concurrent `CreateSocialRegistration` calls, nor the
provider/subject mismatch path, which are separate and still untested.

### Verification

- `go build ./...` — clean.
- `go vet ./...` — clean.
- The focused test, then `-count=10` — **passed every run**, no flake.
- The registration, login and finalization integration tests together — **passed**.
- `go test ./...` — **exit 0**, no failures across all 64 packages.

**The race detector was run**, using the MSYS2 ucrt64 gcc already on the machine's
`Path`. The focused test under `-race -count=5` passed with no data race; every
concurrency test in the suite (`-run Concurrent -race -count=3`, 13 tests) passed;
and the full suite under `go test ./... -race` exited 0 with no data race reported.

Note for anyone reproducing this on Windows: a shell that was already running when
`Path` gained the compiler directory will not see `gcc`, because each child process
inherits that shell's already-resolved environment. Prepending the directory to
`$env:Path` within the command is enough to run the detector; a fresh shell or a
restarted terminal is not required.

---

## Social registration creation — concurrency and identity-mismatch policy

Two correctness defects in `CreateSocialRegistration`, both found by review rather
than by a failing test. `CreateManualRegistration` already handled the first; the
second had no defined behaviour at all.

### Defect 1 — a concurrent pair lost the address race and reported a conflict

Two Google logins for the same new address both find no pending registration.
`GetPendingRegistrationByEmailForUpdate` uses `FOR UPDATE`, which locks a row —
and there is no row yet, so neither request is serialized. Both insert, one wins
`pending_registrations_email_idx`, and the loser is answered
`409 REGISTRATION_ALREADY_EXISTS` carrying **no verification id**. The registration
exists and is correct; the loser simply cannot reach it. A double-tapped sign-in
button or a client retry after a lost response is enough to trigger it.

`CreateManualRegistration` had this exact race and solved it by running
`createManualRegistrationOnce` a second time when, and only when, the failure is
that one constraint. The re-run re-reads the winner's committed row and takes the
ordinary already-pending path, producing the answer a sequential second request
would have got.

**The fix is that same wrapper, applied to the social flow.** It is safe there for
the same structural reason: the re-run re-reads through
`GetPendingRegistrationByEmailForUpdate`, finds the winner's social registration,
reads its `pending_social_identities` row, finds the provider and subject match
(both requests carry the same verified Google identity), and returns the winner's
verification id. Nothing is blind-retried and no error is swallowed — the retry is
gated on `ConstraintName == pending_registrations_email_idx`, and it happens at
most once. A second loss, or any other failure, is still reported as it is.

### Defect 2 — a provider/subject mismatch returned HTTP 500

When a social pending registration already existed for the address but its stored
identity differed from the incoming verified one, the repository returned
`apperror.Internal(...)`. Two problems: a business condition was reported as a
server fault, and the check sat **before** the expiry branch, so a registration
that had already aged out could never recover — the address was permanently
unregistrable through Google, and only a manual registration could break the
stalemate.

### The policy chosen

The stored identity is **never rewritten**, and **a live registration is never
transferred or expired by a different identity**. A mismatch on a registration
that can still be finalized is refused:

| Existing registration                          | Incoming identity | Outcome                                                   |
| ---------------------------------------------- | ----------------- | --------------------------------------------------------- |
| not expired, **same** provider + subject       | same              | reuse it, as before                                       |
| not expired, **different** provider or subject | different         | **`409 REGISTRATION_IDENTITY_MISMATCH`**, no state change |
| expired, **same** provider + subject           | same              | expire and replace, as before                             |
| expired, **different** provider or subject     | different         | expire the dead row and start a fresh registration        |

#### Why a live registration is refused rather than replaced

The capability to finalize lives in the mailbox, not in the row. Whoever reaches
this branch holds a Google-signed assertion that _they_ control the address, and
the PIN is mailed to that same address — so they could already complete the stored
identity's registration if they chose to. Nothing is gained by letting them cancel
it, and something real is lost:

- **It would be a cancel primitive.** Expire-and-replace would let anyone who can
  obtain an address at a provider destroy another identity's in-flight
  registration on demand. The refusal removes that; the caller learns the address
  is held under a different identity and nothing more.
- **It is silent.** The stored owner would find their registration vanished with
  no explanation and no way to tell a race from a takeover. A conflict is at least
  truthful.

The response carries a **dedicated code** rather than the bare `CONFLICT` used for
"registration is no longer pending". That generic conflict means _retry, this was a
race_; this one means _retrying cannot help, this address is claimed by another
identity_, and a client that cannot tell those apart will retry forever. The
message deliberately **does not echo the stored provider or subject**: that would
disclose another identity's internals to a caller who merely presented a matching
address.

#### Why an expired registration is replaced

Once a registration is past `expires_at` it can no longer do anything. Every gate
refuses it: `validateVerificationPending` and `validateRegistrationContinuation`
both reject it on `RegistrationExpiresAt`, and `CompletePendingRegistration`
requires `expires_at > NOW()`. It holds no capability.

So expiring it grants the incoming identity nothing that the passage of time had
not already granted — they could simply wait out the same deadline and be given a
fresh registration. Refusing instead would leave a row nothing can ever consume
and nothing ever deletes (there is no reaper), bricking the address permanently.
The original `pending_social_identities` row is left attached to the original
registration and is never reused, so this is a replacement, **not a transfer**.

#### Why this does not weaken the identity-binding model

The chain that binds an account to a provider subject is unchanged:
continuation token → `pending_registration_id` → `pending_social_identities` →
`auth_identities`. Refusing on mismatch only ever removes a path; it never creates
one. Nothing in this change lets a different subject reach the stored row, and
nothing lets the stored row reach an `auth_identities` insert it was not created
for.

**This is deliberately different from `ConfirmAccountLink`**, which requires a live
Google ID token matching the confirmation's provider _and_ subject because it links
to a **pre-existing account**. No such requirement is added here: this flow creates
a new account and is already gated on mailbox control through the emailed PIN, and
adding a token requirement would change the flow's contract rather than fix a
defect. The account-link confirmation flow is untouched.

### Verification

**The concurrency test was proven to detect what it claims, and the measurement
changed how it is written.**

The first version was a single round of eight concurrent logins. It passed — with
the re-run **removed from the production code entirely**. The window between "the
lookup found no row" and "the insert claims the address" is short enough that one
round usually misses it: the losers find the winner's committed row and take the
reuse path, so the race is never entered. The pre-existing manual-flow equivalent,
`TestRegisterManual_ConcurrentSameNewEmail`, behaves the same way and also passes
with its own fix removed, so this is a property of an HTTP-level barrier rather
than of this test.

Measuring it settled the arrangement:

| Build               | Refused requests across 40 rounds of 8 |
| ------------------- | -------------------------------------- |
| re-run **removed**  | **272 / 320**                          |
| re-run **in place** | **0 / 320**                            |

The test therefore repeats the race over 40 truncated rounds, and each guard was
then removed in turn to confirm the case fails:

- re-run removed → `TestCreateSocialRegistration_ConcurrentSameNewEmailReusesOneRegistration` **fails**
- mismatch check moved ahead of the expiry branch → `..._DifferentIdentityOnExpiredRegistrationReplacesIt` **fails**

Observed responses:

- Concurrent pair: all eight answered `200` `registration_required` with the
  **same** verification id; exactly one pending registration, one pending social
  identity, one verification request.
- Mismatch on a **live** registration: `409 REGISTRATION_IDENTITY_MISMATCH`; stored
  provider, subject and `pending` status all unchanged; no new rows.
- Mismatch on an **expired** registration: `200` `registration_required` for a
  fresh registration, original left `expired` with its own identity — never a 500.
- Same identity on a live registration: reuse, unchanged.
- Manual supersedes pending social: unchanged, and the superseded identity stays
  with the superseded registration.

Full run: `go build ./...` clean, `go vet ./...` clean, `go test ./...` **exit 0**
across 20 packages with no failures, and `go test ./... -race` **exit 0** with no
data race (MSYS2 ucrt64 gcc prepended to `PATH` for the command).

### A fixture mistake worth recording

The first version of the mismatch seeds created a pending registration and a
pending social identity but **no verification request**. Every case then failed
with `REGISTRATION_ALREADY_EXISTS` rather than the expected outcome, because
`GetPendingRegistrationByEmailForUpdate` **inner-joins** `verification_requests`: the
seeded registration was invisible, so the flow fell through to the insert and
collided with its own fixture.

The seeds now write the verification request too. It is worth stating plainly
because the failure mode was misleading — a fixture the production code cannot see
produces a passing-looking conflict for entirely the wrong reason, and only the
assertion on the _code_ separated "the branch I am testing" from "the branch that
could not find my row".

---

## Social registration email source of truth — corrected

`users.email` was being created from `pending_social_identities.email_snapshot`
instead of from `pending_registrations.email`. Two values that are deliberately
different were treated as interchangeable.

### What was wrong

`FinalizeSocialRegistration` read the pending social identity — correctly, for
provider and subject — and then also took the account address from that row:

```go
Email: pendingSocialIdentity.EmailSnapshot.String,
```

The snapshot is provider metadata. `pending_registrations.email` is the
normalized, `NOT NULL`, uniquely indexed address the registration was opened
against and the one every later read compares against. Conflating them had two
consequences:

- **Casing.** A snapshot whose rendering differed from the normalized address
  would create an account that `GetUserForLogin` and `GetUserByEmail` can never
  find, because both compare with exact equality on `TEXT`. The account would look
  correct in the database and be unable to sign in.
- **Nullability.** `email_snapshot` is nullable and `pgtype.Text.String` is `""`
  for `NULL`. A `NULL` snapshot satisfied `users.email`'s `NOT NULL` with an empty
  string, producing an account reachable by no login path at all.

Neither was reachable in production: the Google caller normalizes once and writes
the same value to both columns with `Valid: true` hardcoded, so the two coincided
**by caller discipline rather than by construction**. This was latent, not live —
and it would have become live the moment a second provider or a provider-refresh
path was added.

### The change

Three lines of production code:

1. `FinalizeSocialRegistrationParams` gains `Email string`, with a comment saying
   why it is a parameter rather than something read in the repository: the
   repository already holds the pending social identity, and reading the address
   from that row is precisely the mistake.
2. The service passes `Email: continuation.Email` — `pending_registrations.email`,
   already in hand from `GetRegistrationContinuation`. No new query, and the same
   source the manual flow uses.
3. The repository creates the user from `params.Email`.

`CreateAuthIdentity`'s `EmailSnapshot` passthrough is unchanged and still writes
the provider's value verbatim, including `NULL`. `pending_registrations.email` is
never mutated after insert — every production `UPDATE pending_registrations` sets
only `status` — so reading it before the transaction is sound.

No migration. No schema change. No sqlc regeneration: the existing seed already
takes `Email` and `EmailSnapshot` independently, so the divergent and `NULL`
fixtures needed no new query.

### Verification

Two integration tests, both proven to **fail on the pre-fix code** — each with
`no rows in result set`, because `GetFinalizedSocialRegistrationState` joins
`pending_registrations pr ON pr.email = u.email` and finds nothing once
`users.email` holds the snapshot's value:

- `..._UsesPendingRegistrationEmailNotProviderSnapshot` — seeds
  `pr.email = "alice@example.com"` with `email_snapshot = "Alice@Example.com"`,
  then asserts `users.email == "alice@example.com"` and
  `auth_identities.email_snapshot == "Alice@Example.com"`, i.e. the account takes
  the authoritative address and the provider's rendering survives as metadata.
- `..._NullProviderSnapshotStillFinalizes` — seeds `email_snapshot` as SQL `NULL`
  and asserts finalization succeeds, `users.email` is the authoritative address,
  and `auth_identities.email_snapshot` is `NULL`. The null assertion is on
  `pgtype.Text.Valid`, not on `String`, so it distinguishes SQL `NULL` from a
  stored empty string.

Both also assert provider and subject binding, registration completion and
continuation consumption. The three pre-existing social finalization tests and
their assertions are unchanged.

`go build ./...`, `go vet ./...`, `gofmt` on the touched files, the focused
registration/login integration tests, `go test ./... -count=1` (**exit 0**, 20
packages), and the focused concurrency and finalization tests under
`-race -count=3` (**exit 0**, no data race) all pass.

`gofmt -l` reports `registration/repository.go` as unformatted, but it was
already unformatted at `HEAD` and the deviation is pre-existing, unrelated to this
change. The lines added here are gofmt-canonical. It was deliberately left alone:
reformatting it would bury this change in an unrelated diff.

### Carried forward

A provider-refetch feature may update `auth_identities.email_snapshot`. Nothing
blocks it: the column is nullable, carries no index or constraint, is not tied to
`users.email` by any foreign key or trigger, and is never read by authentication
(`GetAuthIdentity` selects `u.email` from `users`). Fixing this source makes that
feature clean, because the refreshed metadata is no longer entangled with the
account's own address.

---

## Registration rate limiting — implemented

Both endpoints that send a message are limited:
`POST /auth/register/verification/:verification_id/pin` and
`.../resend`. Three independent budgets apply, and none replaces another.

| Budget     | Subject                    | Limit             | Namespace         | Enforced by         |
| ---------- | -------------------------- | ----------------- | ----------------- | ------------------- |
| per-address| normalized email address   | 10 per 24 hours   | `pin-email`       | service, after load |
| global     | the deployment             | 250 per 24 hours  | `pin-email-global`| service, before issue |
| per-IP     | resolved client IP address | 20 per 10 minutes | `pin-ip`          | route middleware    |

The two email budgets are **rolling windows**, shared with the password reset
feature and configured through `PIN_EMAIL_BUDGET_PER_EMAIL_MAX`,
`PIN_EMAIL_BUDGET_GLOBAL_MAX` and `PIN_EMAIL_BUDGET_WINDOW_SECONDS`. The per-IP
budget is unchanged and remains a fixed window.

**`documentation/pin-email-budgets.md` is the reference** for this policy: why
the window rolls, why the per-address budget is shared across the two features,
the ordering, the refund rule, the sender contract and the error codes. The
summary below is deliberately short; read that file before changing any of it.

Both email budgets are shared across both endpoints and both features, so
switching from `/pin` to `/resend` is not a way to double an allowance.

### Ordering within a request

```
IP middleware  ->  handler parses the path  ->  service resolves the verification
  ->  state  ->  cooldown  ->  per-address budget  ->  global budget  ->  issue  ->  send
```

- **The per-IP budget is enforced in front of the handler**, not in the service.
  A service never sees a request that failed to parse its UUID or named a
  verification that does not exist, so a limit enforced there would count only
  well-formed requests. Repeated invalid requests are counted, because the
  cheapest request an attacker can make is one that cannot succeed.
- **The cooldown is checked before both budgets**, so a caller already being told
  to wait is not also charged for asking. With ten a day this matters more than
  the old five an hour: a slot spent on a request that was never going to send is
  one the user cannot get back until tomorrow.
- **The per-address budget is checked before the global one**, so a caller refused
  for its own address does not also spend a slot of the whole deployment's
  budget. The mirror case is the cheaper error.
- **A request the IP budget refuses never reaches either email budget.**

The two registration endpoints that do not send a message
(`POST /auth/register/manual`, `.../verify`, `.../finalize/*`) are deliberately
not client-limited.

### Reservation and refund

Only the global budget is reserved and refunded. **A slot is given back only when
the request failed strictly before the provider could have been contacted** — code
generation failure, issuance transaction failure, or a sender that explicitly
reports it made no attempt. A sender that was reached and then failed keeps its
slot, because a timeout does not prove the message was declined.

IP and per-address counters are never refunded.

### Responses

| Situation                     | Status | Code                                                   | `Retry-After` |
| ----------------------------- | ------ | ------------------------------------------------------ | ------------- |
| per-address budget spent      | 429    | `PIN_RATE_LIMIT_EXCEEDED`                              | yes           |
| global budget spent           | 429    | `PIN_EMAIL_BUDGET_EXCEEDED`                            | yes           |
| client budget spent           | 429    | `IP_RATE_LIMIT_EXCEEDED`                               | yes           |
| email budget unusable         | 503    | `PIN_RATE_LIMIT_UNAVAILABLE` / `PIN_EMAIL_BUDGET_UNAVAILABLE` | **no** |
| code issued, message not sent | 503    | `PIN_EMAIL_DELIVERY_FAILED`                            | yes           |
| client address unresolvable   | 503    | `CLIENT_IP_UNAVAILABLE`                                | **no**        |

**A budget that cannot be evaluated is a refusal, not permission.** Allowing on
failure would let anyone remove the ceiling by causing one. A 503 from an
unevaluated budget names no recovery time because there is none to name;
fabricating a `Retry-After` there would tell a client to come back at a moment
nobody chose. A 503 from a delivery failure *does* name one, because the
issuance transaction restamped the resend cooldown moments earlier.

`Retry-After` is written by `httpx.NewErrorHandler`, the single place a response
is written. A value carried on an error object and never emitted would leave every
client guessing when its own window reopens.

### `Retry-After` semantics

- **Read from the oldest entry still inside the rolling window**, not from the
  key's TTL. The TTL is garbage collection, re-armed to a whole window on every
  call, so a caller arriving late would otherwise be told to wait out time that
  had already passed.
- **Returned by the same Lua script that decided the call.** Reading the count and
  the wait in two round trips would let an entry age out between them, so a caller
  could be told "over budget" from a count that was real and a wait that no longer
  was.
- **Rounded up** to whole seconds, because the header's contract is "not before"
  and rounding down names a moment the window is still closed.
- **Never negative.**
- **A refused request does not grow the window.** Refusals are not recorded at
  all in a rolling window: the window is defined by the entries inside it, so a
  subject at its maximum stays there until its oldest entry leaves the period,
  however many refusals arrive in between. Recording them would grow a set meant
  to hold at most the maximum without bound.

### Client address resolution

The deployment platform is **not decided**, so nothing is assumed about it. The
resolution source is declared explicitly rather than inferred.

| `CLIENT_IP_SOURCE` | Meaning                                                      | Forwarded headers                                           |
| ------------------ | ------------------------------------------------------------ | ----------------------------------------------------------- |
| `peer` (default)   | Clients connect directly; the socket address is the client's | never read, whatever the request contains                   |
| `proxy`            | A verified proxy fronts the application                      | read only from `TRUSTED_PROXIES`, in `TRUSTED_PROXY_HEADER` |

Configuration is refused at startup when it could not be trusted:

- `proxy` with no `TRUSTED_PROXIES` — a forwarded address believed from any
  source, which is the same as believing it from the client;
- `TRUSTED_PROXIES` or `TRUSTED_PROXY_HEADER` set while the source is `peer` —
  a contradiction whose intended half is a guess;
- an entry that is not an IP or CIDR;
- an entry that trusts everything: `0.0.0.0/0`, `::/0`, any range broader than
  `/8`, or the unspecified address;
- **a bare address that is not in canonical form** — long-form or uppercase IPv6,
  or IPv4 written in its IPv6-mapped form.

The last one exists because of how the framework matches. It keeps each
configured address as the text it was given and compares it to the incoming peer
rendered canonically, so `2001:0db8::1`, `2001:DB8::1` and `::ffff:10.0.0.1`
would configure an allowlist that silently matches nothing. The peer is then never
recognised as a proxy, the forwarded address is never read, and **every client
through it resolves to the proxy's own address and shares one counter** — the
exact failure this configuration exists to prevent, with no error anywhere.
Ranges are exempt: they are matched numerically, not by text.

All four framework settings are **derived** from that one declaration by
`config.Config.ProxySettings()`. There is no combination in which a header is
read without a verified allowlist beside it.

`EnableIPValidation` is turned on whenever a header is read, and this is not
optional. Fiber v3.5.0's `extractIPFromHeader` returns the header's **raw bytes**
when validation is off, so a chain of `203.0.113.7, 198.51.100.4` would become
the client's identity — a different counter for every chain the client chose to
send. With validation on, Fiber walks the chain right-to-left skipping trusted
proxies, so a client that prepends a forged entry does not move its own address.

Addresses are canonicalized through `ratelimit.NormalizeIP` before being used as
a subject: ports and IPv6 zones are stripped, and IPv4-mapped IPv6 collapses to
IPv4. Without that, one client behind one NAT gets a separate budget for each
spelling of its address.

An address that cannot be resolved is refused with `CLIENT_IP_UNAVAILABLE`
rather than given an invented subject, because every unresolved request would
otherwise share one counter.

### Verification

`internal/ratelimit` — IP canonicalization and its equivalence classes; policy
validation; `Retry-After` rounding including a sweep proving it is never
negative; fail-closed on an unreachable Redis; key derivation carrying neither
the subject nor its namespace collisions.

`internal/middleware` — 429 with `Retry-After`; 503 with no `Retry-After`;
`Retry-After` rounding across whole, fractional, zero and negative waits;
six different forwarding headers (`X-Forwarded-For`, `X-Real-IP`, `Forwarded`,
`Client-IP`, `CF-Connecting-IP`, `True-Client-IP`) all ignored with no proxy
configured; a configured proxy believed; distinct clients given distinct
identities; a client prepending to the chain not moving its own address; the
configured policy applied; an unparseable forwarded value refused rather than
used.

`internal/config` — the default peer source; proxy mode requiring an allowlist;
every rejection above, including non-canonical bare addresses; canonical bare
addresses and ranges of any spelling still accepted; a copied allowlist that
cannot be mutated through the returned settings; `Load` refusing to start on an
untrustworthy configuration.

`internal/integration` — against real Postgres and real Redis: TTL on first
increment; the TTL not extending on later calls; window expiry restoring the
budget; concurrent requests admitting exactly `Max` (both namespaces, and
repeated across rounds); concurrent HTTP requests against one address admitting
exactly 20; one address exhausted not affecting a neighbour; a verification not
locked by another address's budget; the email and IP budgets refusing
independently; `Retry-After` present, integral, within the window and
non-increasing across refusals; a forced TTL-less counter repaired rather than
reported as a negative wait; Redis failure producing 503 with nothing issued and
no header; spoofed headers landing on the peer's address; registration creation
consulting no limiter at all.

**The concurrency tests were proven to detect the race they claim to.** With the
Lua script replaced by a naive `GET`/`SET` read-modify-write, all three
concurrency tests fail — including the new per-IP one — and pass again with the
script restored.

Nothing here was weakened to make a test pass.

---

## Login rate limiting and enumeration resistance — implemented

`POST /auth/login` and the three Google authentication routes are limited, by
separate budgets under separate namespaces. Two independent budgets apply to
manual login, and neither replaces the other.

| Budget    | Subject                    | Limit             | Namespace        | Enforced by      |
| --------- | -------------------------- | ----------------- | ---------------- | ---------------- |
| per-IP    | resolved client IP address | 30 per 10 minutes | `login-ip`       | route middleware |
| per-email | normalized email address   | 10 per 15 minutes | `login-email`    | `login` service  |
| per-IP    | resolved client IP address | 60 per 10 minutes | `google-auth-ip` | route middleware |

The third row covers `POST /auth/login/google`,
`GET /auth/login/google/account-link/:confirmation_id` and
`POST /auth/login/google/account-link/:confirmation_id/confirm`. It is described
in its own section below.

Both of manual login's budgets are Redis-backed fixed-window counters from the
same `internal/ratelimit` package the PIN budgets use, sharing one Redis, one Lua
script and one connection pool. An address spread across a thousand source IPs is
never near its per-IP ceiling, which is exactly the shape of a credential-stuffing
list; the per-email budget is what still holds in that case.

The per-IP ceiling sits deliberately above the equivalent PIN ceiling. People
share addresses — a household, an office, a carrier's CGNAT — and do so most
heavily at predictable moments, when everyone signs in at once. A login false
positive is worse than a PIN false positive: the user cannot authenticate at
all, and there is no second way in.

### Ordering within a request

```
IP middleware  ->  normalize the address  ->  charge the address budget
  ->  look the account up  ->  verify the password  ->  create the session
```

- **The address budget is charged before the lookup, not counted after the
  failure.** A check-then-count arrangement would let a burst of concurrent
  requests all read "within budget", all start an Argon2id derivation, and only
  be refused afterwards: the limit would bound how many requests are _ultimately
  refused_ while bounding none of the work that costs anything. Charging first
  bounds the work.
- **A charge is handed back when the work it guarded succeeded.** The release
  happens before the session is created. Without it, every successful sign-in
  would spend the failure budget of the address it authenticated, and a
  legitimate user who signed in a few times could not sign in again.
- **A request the IP budget refuses never reaches the address budget.** A flood
  must cost a locked-out account nothing: what was spent was a network budget,
  not the account's.
- **A release that fails does not fail the login.** The budget was enforced when
  it was charged, which is the step that bounds the work and the step that fails
  closed. An unreleased charge makes the address marginally stricter for the rest
  of its window and then expires with it. Refusing the login instead would tell a
  real user their password was wrong while a session row was on its way into the
  database, and they would retry and create more.

### Responses

| Situation                   | Status | Code                                                         | `Retry-After` |
| --------------------------- | ------ | ------------------------------------------------------------ | ------------- |
| per-IP budget spent         | 429    | `IP_RATE_LIMIT_EXCEEDED`                                     | yes           |
| per-email budget spent      | 429    | `LOGIN_RATE_LIMIT_EXCEEDED`                                  | yes           |
| Redis unreachable           | 503    | `IP_RATE_LIMIT_UNAVAILABLE` / `LOGIN_RATE_LIMIT_UNAVAILABLE` | **no**        |
| client address unresolvable | 503    | `CLIENT_IP_UNAVAILABLE`                                      | **no**        |

`LOGIN_RATE_LIMIT_EXCEEDED` deliberately does not say which budget refused. The
path already names the endpoint, and naming the dimension would tell a caller
whether their guess ran into a shared address or into a specific account — more
than the response needs to carry.

`Retry-After` follows the same rules as the PIN budgets: read from the counter's
remaining TTL rather than the configured window, returned by the same atomic
script that incremented the counter, rounded up, never negative, and not extended
by a request that is being refused.

**A limiter that cannot answer is a refusal, not permission.** Allowing on
failure would let anyone remove the ceiling by causing an outage. A 503 names no
recovery time because there is none to name, and the credentials were never
tried: an outage must not become a way to have a password checked.

### Enumeration resistance

Three mechanisms, none of which alone is sufficient:

1. **The lookup reports one thing for two cases.** `GetUserForLogin` joins the
   credential table, so an address with no account and an account with no
   password credential (Google-only) both come back as `INVALID_CREDENTIALS`.
   Neither releases the charge, or an attacker could spend someone's budget at
   leisure by probing addresses that do not exist.
2. **A dummy verification runs when the lookup finds nothing.** Without it, an
   unknown address returns before any password work while a wrong password pays
   for a full Argon2id derivation — a gap large enough to tell an observer which
   addresses exist, and one that also makes enumerating them cheap.
   `PasswordHasher.DummyVerify` performs a real derivation against a fixed hash
   built from the same constants `Hash` uses and computed once through
   `sync.OnceValue`, so it cannot fall behind the active parameters and is not
   re-derived per request. It equalizes the dominant term and nothing else: a
   successful login is still slower, because it adds a session write.
3. **The address is normalized once**, by `registration.NormalizeEmail`, and that
   single value is used for both the budget and the lookup, so the two can never
   disagree about which account is being limited. A budget keyed on the raw
   submission would hand an attacker `Alice@Example.com` and
   `alice@example.com` as two independent allowances for one account.

The endpoint has always returned the same code and message for a wrong password
and for an unknown address. What changed is the cost, not the response.

#### The enumeration defence is asserted directly, not only by the clock

`LoginManual` previously held a concrete `*security.PasswordHasher`, so the only
way a test could observe that a dummy derivation happened was to compare elapsed
times. That is a claim about a shared machine's scheduler as much as about the
code, and it failed: under `go test -race ./...` the assertion reported a
known-account minimum of 228ms against an unknown-address minimum of 71ms from
correct code. Measured directly the two paths cost the same — a ratio of about
1.0 — so the failure was the measurement, not the mitigation.

`internal/api/auth/login` now depends on a narrow `PasswordVerifier` interface
declared in `types.go`, alongside `LoginRateLimiter`, so a test can watch which
of `Verify` and `DummyVerify` the service asked for. `*security.PasswordHasher`
satisfies it unchanged, `module.go` is untouched, and
`var _ PasswordVerifier = (*security.PasswordHasher)(nil)` keeps that honest at
compile time. Placing it in `types.go` rather than `service.go` also means no
generated mock needed regenerating.

Four tests in `dummy_verification_seam_test.go` now assert the property with no
clock involved: an unknown address performs exactly one dummy derivation and
never `Verify`; a known account calls `Verify` and never `DummyVerify`; the
dummy path is reached exactly once per request; and a dummy derivation failure is
not reported as `INVALID_CREDENTIALS`. Removing the `DummyVerify` call fails
three of the four immediately.

The timing test is kept as a secondary end-to-end signal and was **not**
weakened: `minimumShare` is still 3 and `runs` is still 5. What changed is that
it now runs a warm-up derivation before timing anything and measures both paths
in **both** orders, keeping each one's fastest. Measured in a single order, the
path that went first paid to grow the heap that Argon2id's 64 MiB per derivation
keeps demanding, which was the whole of the flake.

### Argon2id parameter bounds

`internal/security` bounds what a stored hash may ask the server to do: `m ≤
128 MiB` and `t ≤ 8`, against active parameters of `m = 64 MiB`, `t = 3`, `p =
4`. Both are enforced in `parseArgon2Params`, **before** argon2 is reached —
`argon2.IDKey` allocates its memory up front, so an unbounded value is an
unbounded allocation and the process does not survive it. The ceilings are
absolute rather than multiples of the active constants so that they cannot
quietly become meaningless, and `TestActiveParametersFitWithinBounds` fails if
the active constants are raised past them, turning "the verifier stopped
accepting new hashes" into a build failure rather than a production incident.

**There are deliberately no lower bounds.** A hash weaker than the active
parameters must still verify, or a credential written before the parameters were
strengthened would lock its user out permanently:
`PasswordVerificationResult.NeedsRehash` exists but nothing acts on it yet, so
there would be no way back in short of a reset. A floor is a product decision
about those users, not a parser decision.

The ceiling has to stay survivable, because the per-client login limiter bounds
how many verifications a caller may _start_, not how expensive each one is. A
verification at the ceiling costs about 128 MiB across eight passes — roughly
five times a legitimate one.

### Verification

`internal/security` — memory, iteration and parallelism ceilings enforced before
the derivation; the ceilings themselves accepted inclusively; a
weaker-than-active hash still verifying and reporting `NeedsRehash`; the active
parameters fitting within the ceilings.

`internal/api/auth/login` (service) — the budget charged on the canonical address
and released only on success; a wrong password and an unauthenticable account
both keeping their charge; a spent budget refusing before the lookup (no
`GetUserForLogin` expectation is registered, so the test fails if the lookup is
reached at all); an unusable limiter failing closed with no `Retry-After`; a
release failure not failing the login; the enumeration floor, measured as the
fastest of five known-account and five unknown-address logins in both
measurement orders, after a warm-up derivation; and, without any clock at all,
that an unknown address calls `DummyVerify` exactly once and never `Verify`,
that a known account does the reverse, and that a dummy derivation failure is
not reported as `INVALID_CREDENTIALS`.

`internal/api/auth/login` (routes) — the declared policy applied in front of the
handler, the subject canonicalized, a spent budget stopping the request, an
unusable one producing 503, and no route spending another flow's budget.

`internal/integration` — against real Postgres and real Redis: the per-email
budget exhausting on the eleventh attempt with nothing issued; `Retry-After`
present, integral, within the window and non-increasing across refusals; one
address's guesses not spending another's; one address spelled five ways drawing
on a single budget; a successful login restoring it; both 503 paths carrying no
header and issuing nothing; the per-IP budget refusing without spending the
per-email one; one address exhausted not affecting a neighbour; concurrent
requests from one address admitting exactly the budget; spoofed headers landing
on the peer's address; and the two per-IP budgets staying separate in both
directions.

**The two tests that measure rather than assert were proven to detect what they
claim to.** With the Lua script replaced by a client-side `GET`/`SET`
read-modify-write, the concurrency test fails; with `DummyVerify` removed, the
enumeration floor fails. Both pass again once restored.

Nothing here was weakened to make a test pass.

---

## Google authentication rate limiting — implemented

All three Google authentication routes are limited by one per-client-address
budget under the `google-auth-ip` namespace:

| Route                                                      | Method |
| ---------------------------------------------------------- | ------ |
| `/auth/login/google`                                       | POST   |
| `/auth/login/google/account-link/:confirmation_id`         | GET    |
| `/auth/login/google/account-link/:confirmation_id/confirm` | POST   |

| Budget | Subject                    | Limit             | Namespace        | Enforced by      |
| ------ | -------------------------- | ----------------- | ---------------- | ---------------- |
| per-IP | resolved client IP address | 60 per 10 minutes | `google-auth-ip` | route middleware |

One middleware instance is mounted on all three, so the whole flow spends one
budget. A caller refused on Google login is not someone who should be handed a
fresh allowance by moving to the account-link routes. Mounted in front of the
handler for the same reason as everywhere else: a body that does not parse, an id
that is not a UUID and a token that does not verify are all counted, and all
three are the cheapest requests to send.

### Why a budget is needed when there is no secret to guess

The credential here is a signature Google made, so an _authentication_ budget has
nothing to stop — there is no password to brute-force. What the routes are not
free to serve is cost, and nothing else bounded any of it:

- Every request verifies an ID token against Google's published keys. The
  verifier's cert cache is process-wide and normally warm, but the cost is
  real, and it is reachable with no credentials at all.
- A caller holding **one valid token** can otherwise make the application write
  `sessions` and `refresh_tokens` rows for as long as it keeps asking. There is
  no per-user session cap on any login path.
- Every `account_link_required` outcome inserts a new
  `account_link_confirmations` row, with no dedup, no cap and no reaper.
- Every `registration_required` outcome runs a multi-statement transaction
  creating a pending registration, a pending social identity and a verification
  request.

So this is a ceiling on resource cost, deliberately _not_ on credential guessing,
and it is placed on the IP because that is the only dimension the caller can vary
without also giving up the thing being rate-limited.

### Why there is no per-email budget here

The email this flow would key on is the one inside Google's verified claim, and
the caller does not choose it: reaching that code at all required a token Google
signed asserting, with `email_verified`, that its holder controls that address. An
attacker cannot aim such a budget at a victim's mailbox.

A per-email budget here could therefore only ever be spent by the legitimate
owner of the address it names. Its only effect would be locking that owner out of
one of two ways in — a denial of service, not a protection. The same reasoning
applies to `ConfirmAccountLink`, where the only address in play is the account's
own, reached through the provider and subject the confirmation already records.

### Why it is separate from the manual-login budgets

`login-ip` and `google-auth-ip` are two doors into the same application, not two
rooms in it. Someone signing in with Google has not spent anything on their
manual-login budget, and someone signing in with a password has not earned a
Google one. Merging them would mean one flow's flood locked the other flow's users
out — and because the limiter fails closed, an application that counted both under
one namespace could have both doors taken down by a single exhausted counter.

### Responses

Identical to the manual-login per-IP budget, because it is the same middleware:

| Situation                   | Status | Code                        | `Retry-After` |
| --------------------------- | ------ | --------------------------- | ------------- |
| budget spent                | 429    | `IP_RATE_LIMIT_EXCEEDED`    | yes           |
| Redis unreachable           | 503    | `IP_RATE_LIMIT_UNAVAILABLE` | **no**        |
| client address unresolvable | 503    | `CLIENT_IP_UNAVAILABLE`     | **no**        |

A 503 means the ID token was never verified. An outage must not become a way to
have a Google credential checked, and it must not become a way to write a session.

### Thresholds are estimates, not measurements

**60 per 10 minutes is an initial engineering estimate.** No deployment platform
has been selected and no traffic has been observed, so neither this value nor the
manual-login one has been validated against real usage or against shared-address
false positives. It is set at twice `LoginIPRateLimitMax` deliberately: a Google
refusal is never a guess running out of attempts, so every refusal here is a false
positive, and Google login is one of only two ways in. 60/10min still caps a single
address at roughly eight thousand requests a day. Reassess both values — and the
shared-proxy failure mode described under outstanding deployment work — before
this is relied on.

### Verification

`internal/api/auth/login` (routes) — all three routes spending one budget under
the declared policy and namespace; a request within budget reaching the handler
with the manual-login budgets untouched; a spent budget stopping the request
before the handler with a bounded `Retry-After`; an unusable budget producing a
503 with no header; and namespace independence in both directions — a spent Google
budget refusing only the Google routes, and a spent manual-login budget leaving
Google login answerable. The independence tests set one namespace's outcome and
let the other default, so a limiter refusing everything cannot make them pass for
the wrong reason.

`internal/integration/auth_login_google_rate_limit_test.go` — against real Redis:
the budget walked to exhaustion, the other two routes then refused on the same
counter, one address exhausted not affecting a neighbour, `Retry-After` integral,
within the window and non-increasing across refusals, concurrent requests from one
address admitting exactly the budget, a spent Google budget leaving manual login
answerable, and a spent manual-login budget leaving Google login answerable.

Every request in that file is deliberately malformed, so the budget is spent in
front of the handler and the flood costs the suite no token verifications and no
database writes. The 400 a malformed request produces is the unambiguous marker
that the limiter let it through.

`internal/integration/auth_login_ip_rate_limit_test.go` — the Google routes
charged three times against `google-auth-ip` and never against `login-ip`.

**Redis concurrency is genuinely covered** for this budget:
`TestGoogleAuthIPRateLimit_ConcurrentRequestsCannotExceedTheBudget` fires three
times the allowance simultaneously and asserts that exactly the excess was
refused, which is the property a read-modify-write counter would fail. It has been
run under `-race` and re-run with `-count=4` without a flake.

---

## Account-link confirmation authorization — implemented

`POST /auth/login/google/account-link/:confirmation_id/confirm` now requires the
Google ID token of the identity being linked, and a `confirmation_id` on its own
authorizes nothing.

### What the confirmation id was, and what it is

The confirmation id used to be a bearer credential. It was the only thing the
endpoint checked, and the endpoint's whole effect was to write
`auth_identities` — a permanent second way into an existing account — and issue a
session. **It was also carried in a URL path**, which is written to access logs,
browser history and `Referer` headers. Fifteen minutes of an arbitrary person's
account, recoverable by anyone who had merely seen the id.

It is now a **flow identifier**. It names which pending confirmation is being
completed; the credential that authorizes completing it is the Google identity
the confirmation was created for, re-proved at the moment of confirmation.

### Why requiring the token again is a meaningful change

The obvious objection is that it adds nothing: creating a confirmation already
required a verified Google token, so anyone who can mint one can also complete
what they minted. That is true and is not the point. The point is that the check
**decouples the two halves of the flow**. Before, the id leaked by itself
converted into a login. After, it does not — the attacker needs the credential
as well, and the half that leaks by accident is the half that no longer matters.

### What the token does and does not prove

It proves the caller still controls **this Google identity**. It does not prove
anything about the pre-existing manual account: that account was matched because
Google asserted, with `email_verified`, that this identity's address is the one
the account holds, and re-asserting that keeps the two halves of the flow
belonging to the same person.

**Requiring the account's password is deliberately not done.** This application
already treats mailbox control as account authority — `POST /auth/password-reset`
is unauthenticated and issues a PIN to the account's address. Re-proving that
same control is consistent with the rest of the design; asking for a password
would be _stricter than password reset_, would break anyone who cannot recall it,
and would train users to type a password into a "link Google" prompt, which is a
phishing shape. If the product ever wants linking to require the pre-existing
credential, that is a deliberate change to the trust model and not a patch.

### The comparison

Both fields, exactly:

| Field              | Source of truth                               | Why                                                                                                                                            |
| ------------------ | --------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `provider`         | `account_link_confirmations.provider`         | Data, not code — it is read back out and copied into `auth_identities`. A row naming another provider must not be satisfied by a Google token. |
| `provider_subject` | `account_link_confirmations.provider_subject` | The identity the link is actually made on.                                                                                                     |

Email is **never** compared. It is not an identity, and treating it as one would
accept a different account that happens to share an address.

The comparison is made outside the transaction, which is safe because neither
column is ever written — the only `UPDATE` against the table sets `confirmed_at`.
The transaction still re-checks that the confirmation is active and unused before
consuming it, so a caller that wins the race to read and loses it to consume is
refused there rather than linked twice.

### No schema change was needed

`account_link_confirmations.provider` and `.provider_subject` were already
`NOT NULL`, already immutable, and already selected by both
`GetActiveAccountLinkConfirmation` and `GetAccountLinkConfirmationForUpdate`. No
migration, and no regeneration of the production sqlc target.

### Responses

| Situation                                         | Status | Code                                |
| ------------------------------------------------- | ------ | ----------------------------------- |
| `id_token` absent, empty, or not bindable         | 400    | `VALIDATION_ERROR` / `BAD_REQUEST`  |
| token missing a claim, invalid signature, expired | 401    | `INVALID_GOOGLE_TOKEN`              |
| valid token, different identity                   | 409    | `ACCOUNT_LINK_IDENTITY_MISMATCH`    |
| confirmation unknown, expired, or already used    | 409    | `ACCOUNT_LINK_CONFIRMATION_INVALID` |
| identity already linked by another confirmation   | 409    | `AUTH_IDENTITY_ALREADY_EXISTS`      |
| linked, but the session could not be created      | 503    | `ACCOUNT_LINK_SESSION_FAILED`       |

`ACCOUNT_LINK_IDENTITY_MISMATCH` is deliberately distinct from
`INVALID_GOOGLE_TOKEN` because the two ask for opposite things: an invalid token
means obtain a new one, a mismatch means retrying will never help. A client that
cannot tell them apart sits in a retry loop. Neither message names the stored
identity or the target account.

`ACCOUNT_LINK_SESSION_FAILED` exists because the alternative is a lie the client
acts on. The link is committed and the confirmation consumed before the session
is created, so retrying the confirmation can only ever return
`ACCOUNT_LINK_CONFIRMATION_INVALID` — telling the user their confirmation was
wrong when the link in fact succeeded. Retrying `POST /auth/login/google` does
recover, because the identity is now linked and that route authenticates it
directly. It carries no `Retry-After`: recovery waits out nothing.

### Frontend contract changes

Both are breaking changes to the API.

1. `POST .../confirm` requires a JSON body `{"id_token": "..."}`.
2. `GET .../account-link/:id` returns **`masked_user_email`** in place of
   `user_email`, and no longer returns `email_snapshot` or `user_display_name`.

The frontend should read the masked address as a hint only. The backend already
made the email match, so the screen is a yes/no gate rather than a picker, and a
masked address leaves the user nothing to mis-pick.

### `email_snapshot` removed — migration 000026

The column was dropped from `account_link_confirmations` by migration
`000026_drop_email_snapshot_from_account_link_confirmations`. It held the Google
address the confirmation was created from, and the account the confirmation points
at is by definition the account holding that same address, so it was a second copy
of a value the row already points to.

The column on `auth_identities` **remains** and is still written. The value now
comes from `users.email` on the account the confirmation joins to, which is the
same string by construction: the account was found by looking that exact
normalized address up. Dropping the confirmation's copy therefore loses nothing,
and `TestConfirmAccountLink` asserts the identity still carries it, with the
fixture's two addresses deliberately different so the source is observable.

`display_name_snapshot` is untouched and stays: it is the Google profile name as
it read when the confirmation was created, and it is not interchangeable with the
account's own `users.display_name`.

`users.display_name` was removed from both confirmation queries. It was selected
but read by nothing — the screen shows `display_name_snapshot`, and the response
has no field for it — so it was dead weight in the generated row structs rather
than a field kept for a future caller.

**`auth_identities.email_snapshot` and `pending_social_identities.email_snapshot`
are different columns on different tables and were not touched.** Migration 000019
added both, and they remain.

### Verification

`internal/api/auth/login` (service) — a matching identity succeeds; an invalid
token reaches no repository call at all (gomock strictness is the assertion);
a different subject and a different provider are each refused with the mismatch
code; the confirmation's stored values are what is compared rather than the
token's; an inactive confirmation is refused before the comparison; and both
post-commit failures — user lookup and session creation — are reported as
`ACCOUNT_LINK_SESSION_FAILED` with the underlying fault retained.

`internal/api/auth/login` (routes) — six malformed or absent bodies are refused,
distinguishing the two codes binding produces; a well-formed body reaches the
token check. **The route test app was given the struct validator it was missing**,
which it had never had: without it Fiber silently skips every `validate` tag, so
these assertions would have passed against a build that validated nothing. The
production binary and the integration app already configured it.

`internal/api/auth/login` (masking) — twelve cases through the service, each
asserting the output is never the input, plus a check that the marshalled
response carries neither the old field names nor a full address.

`internal/integration/auth_login_account_link_test.go` — against real PostgreSQL:
a matching identity is accepted; a different identity and a different provider
are each refused with nothing written; a rejection leaves the flow usable; a
missing token writes nothing and the confirmation still works afterwards; single
use leaves one identity and one session; an expired confirmation is refused by
both routes; eight concurrent attempts produce exactly one link and exactly one
session with every other answered as a spent confirmation; and a second
confirmation for the same identity loses with `AUTH_IDENTITY_ALREADY_EXISTS`,
which is the one path that previously had no coverage.

**The concurrency test's strength was measured, not assumed.** With `FOR UPDATE`
removed from `GetAccountLinkConfirmationForUpdate`, a _single_ run still passed —
the unique index on `(provider, provider_subject)` is independently enough to keep
the losers failing — and only repeated runs caught it. So the test verifies the
outcome under contention, not which of the three guards produces it. It is
documented as needing `-count` for that distinction and has been run at
`-count=5` under `-race` without a flake.

### Known gaps, reported rather than acted on

- **`user_display_name` is still returned in full.** It is the pre-existing
  account's display name on an unauthenticated route. Reducing it was considered
  and deferred: this task named the email specifically, and changing an
  unrequested field would widen the contract change the frontend has to absorb.
- **`ACCOUNT_LINK_SESSION_FAILED` is implemented but not covered end to end.**
  The service unit tests assert the mapping; forcing a real session-insert failure
  through the HTTP app would need fault injection this suite has no facility for.
- **Expiry still leaves rows behind.** Expired and confirmed confirmations are
  never deleted, so the table grows without bound. Unchanged by this task.
- **A caller who loses a Google token mid-flow must re-prompt.** Not a defect,
  but it is a UX cost this change introduces and the frontend must handle it:
  a 401 on confirm means "re-obtain the token", not "wrong confirmation".

---

## Bounded password-work capacity — implemented

Rate limiting and password work are different controls, and only the first of
them is a rate limit.

A rate limit bounds how often one subject may submit requests. It does not bound
how much expensive work is running right now, because a limit per subject
composes: a caller holding a thousand addresses holds a thousand budgets, and
every one of them can be spent at the same moment. On a constrained instance the
callers causing the memory pressure are frequently not the ones being limited.

### The mechanism

`internal/security.PasswordWorkLimiter` bounds how many derivations run at once
across every subject and every feature. It keeps no queue, no per-caller state
and no goroutine of its own.

| Setting                   | Env                         | Default | Range   |
| ------------------------- | --------------------------- | ------- | ------- |
| `PasswordWorkConcurrency` | `PASSWORD_WORK_CONCURRENCY` | `1`     | 1–64    |
| `PasswordWorkWait`        | `PASSWORD_WORK_WAIT_MS`     | `1000`  | 0–60000 |

Both are range-checked at startup and a supplied-but-unusable value stops the
process rather than being corrected — the same rule `WORKER_CONCURRENCY` follows.
The ceilings are typo guards, not tuning values: at the active parameters each
derivation costs 64 MiB, so even the ceiling is more than a small instance could
hold.

`Acquire` returns the release belonging to that acquisition rather than the
limiter exposing an unowned `Release()`. That pairing is the ownership guarantee:
a release that runs again for an acquisition that already completed would
otherwise hand back a slot a different, live operation was relying on, and the
limiter would admit one more derivation than its limit. The three outcomes are
kept separable — success, `ErrPasswordWorkCapacityTimeout`, and `ctx.Err()` —
because a caller that went away must not be told it was merely busy.

### Where it is enforced

Inside the shared `PasswordHasher`, not at the call sites. `Hash`, `Verify` and
`DummyVerify` all take a slot and give it back, so a call site cannot reach
Argon2id without passing through the bound. One limiter is shared by login and
registration; two would each allow their own number and the total would be the
sum.

The dummy verification is bounded too, and it has to be: it costs exactly what a
real verification costs, so a mitigation exempt from the bound would be the
cheapest way to hold more derivations at once than the instance was built for.

`Verify` decodes the stored hash before acquiring, so a row nobody can parse
cannot hold a slot against every real request.

### Responses

| Situation                 | Status | Code                        | `Retry-After`            |
| ------------------------- | ------ | --------------------------- | ------------------------ |
| no password-work capacity | 503    | `PASSWORD_WORK_UNAVAILABLE` | yes, the configured wait |

It is neither `INVALID_CREDENTIALS` nor a rate-limit code. The password was never
checked, so reporting a bad password would be a lie the caller acts on, and
reporting a rate limit would name a budget that was not spent and a window that
does not exist. A caller that went away keeps its own context error rather than
being converted to this.

Password reset's `Hash` is bounded too — it goes through the same hasher — but
its refusal is not yet translated into this response; it is currently reported as
an internal fault. That is the next task.

### Verification

`internal/security` — every operation refusing when the slot is held from
outside, a refused operation returning nothing usable, cancellation and an
unparseable hash each reported as themselves rather than as exhaustion, capacity
returned on success, refusal and error paths, `DummyVerify` not re-entering the
limiter (which at concurrency one would be a deadlock rather than a slow request),
the reported wait, and a hasher refusing to be built without a limiter.

`internal/api/auth/login` and `internal/api/auth/registration` — capacity
exhaustion producing 503 with a bounded `Retry-After` on both the known-account
and unknown-account paths, not being mistaken for invalid credentials, a rate
limit, an input error or a fault, the public message describing none of the
mechanism, and the same request succeeding once capacity returns with the email
budget still accounting exactly one failure for the refused attempt and one
release for the successful one.

Every one of these holds the single slot from the test rather than waiting for a
real derivation, so none depends on how long Argon2id takes on the machine.

---

## Password reset enumeration resistance and IP rate limiting — implemented

Two changes, in the order they were made. The first removes the account-enumeration
signal from `POST /auth/password-reset`; the second bounds how fast any client
address can use this flow at all.

### Dummy verification IDs

`POST /auth/password-reset` used to answer an address no account holds with
`404 RESOURCE_NOT_FOUND` and an address that is held with `201` and a real
verification id. That is a binary, free-to-probe, unauthenticated oracle, and the
id it returned was not merely a hint — it was the credential the rest of the flow
is written against.

It now returns `201` and the same body for both:

- **A known address** opens or reuses a password reset exactly as before and gets
  the real persisted verification id.
- **An unknown address** gets a fresh UUIDv7 that is **never persisted**. No
  `pending_password_reset`, no `verification_request`, no verification code, no
  email.

The placeholder is a **v7** UUID because the real ones are (`uuidv7()`, migration
000006). A v4 placeholder would be distinguishable from a real id by its version
nibble alone, which would move the signal out of the status code and into the body
rather than removing it. No production code inspects UUID version; the v7 choice is
purely so a client cannot tell the two apart by inspection.

Because nothing backs the placeholder, it is an ordinary unknown id to everything
downstream. `GET /password-reset/verification/:id`, `POST .../pin`,
`POST .../resend` and `POST .../verify` all resolve it, find no row, and refuse it
through the same path any made-up id takes — `404`. Finalization takes a
continuation token rather than a verification id, and that lookup inner-joins
`users`, so a placeholder presented as one resolves to nothing (`409`). A dummy id
can therefore never verify a PIN, mint a continuation, write a credential, or
revoke a session. This is asserted against the database tables rather than
inferred from a status code.

`404` is no longer documented for this route; the Swagger description states that
the response does not distinguish the two cases.

### The accepted residual channel

**Repeated requests for a known address with an open reset return the same
persisted verification id, while an unknown address returns a fresh placeholder
every time.** An attacker posting twice to one address inside the fifteen-minute
reset window learns whether an account exists, with no timing measurement needed.

This is accepted for now, because removing it is a product change rather than a
bug fix:

- **Reuse is required behaviour.** A second request must return the open reset
  rather than opening another, or it would reset the window a user is waiting out.
  That requirement is why the two cases differ observably.
- **Always minting a fresh id** would satisfy the oracle and break reuse: the id a
  client holds would stop being the id its flow runs under.
- **Omitting the id entirely** would satisfy the oracle but leaves the frontend
  with no way to address PIN entry or verification, all of which take a
  verification id in the path. Closing this properly means redesigning how the id
  reaches the client — not changing a status code or a response shape.

The two responses are otherwise byte-identical, so status and body no longer carry
the signal at all. One deterministic channel remains where three easy ones did.

A smaller timing signal also remains and is not addressed here: a known address
costs one indexed lookup more than an unknown one, because it continues to the
second lookup and, when no reset is open, an insert transaction. This gap
predates the change and its magnitude is unchanged; only the trivially-exploitable
status-code oracle was removed.

### IP rate limiting

Three endpoint groups are budgeted per client address. Each has its own namespace
and its own policy, because opening a reset, reading one, and mailing a code do
not cost the same thing, and a flood of one must not spend another's allowance.

| Group | Routes | Namespace | Policy |
|---|---|---|---|
| Request | `POST /auth/password-reset` | `password-reset-request-ip` | 10 / 10 min |
| Status | `GET /password-reset/verification/:verification_id` | `password-reset-status-ip` | 60 / 10 min |
| PIN issuance | `POST .../pin` **and** `POST .../resend` | `password-reset-pin-ip` | 20 / 10 min |
| Verification | `POST .../verify` | `password-reset-verify-ip` | 20 / 10 min |
| Finalization | `POST /auth/password-reset/finalize` | `password-reset-finalize-ip` | 10 / 15 min |

**Why these values.**

- **Request, 10/10 min.** This is the account-probing surface. A reset request
  sends no message and does no password work — for an unknown address it is one
  indexed lookup and nothing else — which is exactly what makes it cheap enough to
  probe in bulk. Half the PIN issuance value is deliberate. It is not tighter,
  because people share addresses and a false positive here is soft: the caller is
  told to wait, nothing is deleted, and the reset lives fifteen minutes, so ten
  requests cannot be spent before a first attempt has expired.
- **Status, 60/10 min.** This route has no side effect at all — one indexed read —
  and it is the route a client re-reads while a flow is open, so a value chosen for
  issuance's cost would refuse a legitimate caller watching its own screen. Three
  times the issuance budget is what polling costs.
- **PIN issuance, 20/10 min.** Deliberately identical to registration's `pin-ip`:
  same act, same cost (a code generated, a row written, a message sent), and
  matching values is what makes the two features comparable. What differs is what
  stands behind it — registration also has a per-address email budget of five an
  hour, and password reset has none yet, so this counter is currently the **only**
  ceiling on what one address can be mailed. That is an argument for keeping the
  value where it is rather than lowering it: a tighter limit here would have no
  second dimension to fall back on, and a false positive would stop issuance
  outright.

**One budget for `/pin` and `/resend`.** Both are mounted behind a single
middleware instance, so both spend `password-reset-pin-ip`. Naming either route
separately would let a caller double its allowance by switching endpoints, and
switching is free because neither route requires authentication and both reach the
same issuance helper. This mirrors registration's handling of the same pair.

**Mounting.** `middleware.IPRateLimit` runs before the handler, so a refused
request never reaches feature logic. A request whose path parameter is not a UUID,
or whose address does not exist, is still counted — and probing addresses that do
not exist is the cheapest request to send.

### Redis failures and trusted proxies

The limiter **fails closed**. An unreachable counter is a refusal, not permission:
`503 IP_RATE_LIMIT_UNAVAILABLE` with **no `Retry-After`**, because the counter has
no window to name and inventing one would tell the client to come back at a moment
nobody chose. This widens the blast radius of a Redis outage — it now stops three
more routes — and the widening is deliberate. A request over budget is
`429 IP_RATE_LIMIT_EXCEEDED` carrying a `Retry-After` read from the counter's own
remaining TTL, which is why that assertion runs against real Redis rather than a
fake that could simply be told what to report.

Client addresses are resolved through the existing `CLIENT_IP_SOURCE` and
`TRUSTED_PROXIES` configuration; nothing about a forwarded header is believed
implicitly. `app.Test` serves from an in-memory connection whose only address is
the unspecified one, so the integration tests drive addresses through an app built
as if behind a verified proxy — the same arrangement registration uses.

**These are initial engineering estimates, not production-validated thresholds.**
No deployment has been selected and no traffic observed.

### Not covered — pending review

`POST /auth/password-reset/verification/:verification_id/verify` and
`GET /auth/password-reset/continuation` are **outside this change and pending
their own security review.** They are mounted with no middleware, unchanged, and
nothing about them should be read as having been assessed.

`POST /auth/password-reset/finalize` was in this list too. It is no longer — see
below.

---

## Password reset finalization: a budget and a capacity mapping — implemented

Two changes, in the order they were made. The first bounds how often one client
address may reach the route; the second gives the refusal the shared password-work
limiter can produce a truthful answer.

### A budget for `POST /auth/password-reset/finalize`

`password-reset-finalize-ip`, **10 per 15 minutes** per client address.

`/finalize` is the only route in this flow that performs an Argon2id derivation, and
it was the last unbounded route that could reach one. The reasoning is the one
registration applied to the same route:

- `security.PasswordWorkLimiter` bounds how many derivations run **at once**
  across the whole process. That protects the process.
- It does **not** stop one caller from holding every slot indefinitely and
  re-acquiring on release. A caller holding a valid continuation token could keep
  the pool occupied and turn every legitimate login, manual registration and
  password reset into a refusal for as long as they kept it there. `registration/constants.go`
  states this explicitly for `finalize-ip`, and the same applies here.

The values are **deliberately identical to registration's `finalize-ip`** — same
route, one derivation per request, same cost to serve. Matching is what makes the
two features comparable rather than merely similar.

The namespace is its own. `finalize-ip` is registration's, and a namespace is one
flow's budget: a caller who has exhausted their password-reset allowance has spent
nothing of registration's, and merging them would let one flow's flood lock the
other out. It is also separate from `password-reset-request-ip`,
`password-reset-status-ip` and `password-reset-pin-ip`, so that exhausting the one
expensive route cannot be used to starve the steps leading to it, and so that a
caller who mistypes a PIN cannot spend the allowance that governs the derivation.

It is mounted **in front of** the handler, so a request carrying no continuation
token, or one that is already consumed, is still charged — and, more to the point,
so a refused request never reaches the hashing step at all.

**These thresholds are initial engineering estimates, not production-validated
values.** No deployment has been selected and no traffic observed. Revisit them
against real traffic and real shared addresses. By count, this and
registration's `finalize-ip` (10) are the tightest budgets in the application;
`password-reset-request-ip` shares the count but opens its window twice as often.

### `PASSWORD_WORK_UNAVAILABLE` on password reset

`FinalizePasswordReset` previously mapped every failure from
`passwordHasher.Hash` to `500 INTERNAL_SERVER_ERROR`. Capacity exhaustion is not a
fault, so a momentarily busy server reported itself as a broken one, and the
response carried **no `Retry-After`** — leaving a client told only "500" with no
instruction and nothing to act on.

It now returns `503 SERVICE_UNAVAILABLE` with `PASSWORD_WORK_UNAVAILABLE`, the
message `the server is busy, please try again shortly`, and a `Retry-After`
derived from `PasswordHasher.Wait()` — the same number the caller was actually
kept waiting for, read from the limiter rather than repeated as a constant. The
value matches `login`'s and `registration`'s deliberately: it is the same
condition with the same recovery for every caller that performs password work,
while the declarations stay in separate packages so a change to one cannot
silently change another.

**Only the capacity timeout becomes this.** `PasswordWorkLimiter.Acquire` reports
a cancelled context and an exhausted budget through the same return value and they
ask the caller to do opposite things — come back shortly, or stop — so a cancelled
request or an expired deadline stays a `500` and carries no `Retry-After`.

**Nothing is decided when hashing is refused.** The refusal happens before the
finalization transaction opens, so the continuation is not consumed, the pending
reset is not completed, no credential is written and no session is revoked. That
is asserted against the tables rather than inferred, and the capability is shown
to still work by completing the same reset once capacity returns.

### Verification

**Unit** — `internal/api/auth/password_reset/password_work_service_test.go`, five
tests over a hasher whose only slot is held from outside the request, so nothing
waits for a real derivation:

- `CapacityExhaustionIs503` — `503`, `PASSWORD_WORK_UNAVAILABLE`, the literal
  string matched against login's and registration's, and a `Retry-After` that is
  present, positive and no greater than the configured wait.
- `CapacityExhaustionIsNotAnythingElse` — not `429`, not `400`, not `401`, not `500`.
- `CapacityExhaustionSaysNothingInternal` — the message is exactly the public one
  and names no mechanism.
- `SucceedsOnceCapacityReturns` — the identical request succeeds after the slot is
  released.
- `CancelledContextIsNotCapacityExhaustion` — a cancelled request is **not**
  reported as a busy server; it stays `500` and carries no `Retry-After`.

The atomicity assertion is an **absence**: `UpsertPasswordCredentialAndCompleteReset`
is never stubbed on any refusal path, and gomock fails on an unexpected call, so
the refusal is proven to happen before the finalization transaction is entered. In
`SucceedsOnceCapacityReturns` the expectation is registered only *after* the
refusal, so registering it up front would have permitted the refusal to take it.

**Integration** — `internal/integration`, six tests:

- `FinalizeBudgetIsEnforcedAheadOfTheHandler` — the first `Max` requests answer
  `401` (reaching the handler) and the next answers `429` +
  `IP_RATE_LIMIT_EXCEEDED`, so the middleware and the handler cannot be confused
  for one another. The namespace, the subject and the policy are read from the
  limiter's own accounting, and registration's `finalize-ip` is asserted
  **untouched**.
- `FinalizeExceededCarriesRetryAfter` — the wait proven against **real Redis**,
  because a fake can be told what to report; it is positive, never exceeds the
  window it came from, and never extends it.
- `FinalizeRefusalConsumesNothing` — a valid token presented from an exhausted
  address is refused `429`, and the tables are then read to show the reset still
  `pending` and no credential written. The capability is then shown to be intact
  by completing the same reset against a fresh limiter.
- `FinalizeOneAddressDoesNotSpendAnother` — one address's flood does not lock a
  neighbour out of the same continuation.
- `FinalizeRedisFailureFailsClosed` — an unusable limiter gives `503`
  `IP_RATE_LIMIT_UNAVAILABLE` with **no fabricated `Retry-After`**, and changes
  nothing.
- `PasswordWorkCapacityIs503` — driven over HTTP against the saturated
  `saturatedApp`: `503`, `PASSWORD_WORK_UNAVAILABLE`, and `Retry-After` equal to
  `ceil(configured wait)`.

**State preservation** is asserted from the tables, not inferred from a status
code, and the two refusal paths assert different sets because they are seeded
differently:

- `PasswordWorkCapacityIs503` seeds a real credential and an active session, so it
  can assert all four — `consumed_at` still null, the reset still `pending`, the
  stored hash still verifying against the **old** password, and the user's session
  still active.
- `FinalizeRefusalConsumesNothing` seeds no credential, so it asserts what that
  fixture can show — the reset still `pending` and `password_credentials` still
  empty for the address.

Both then complete the same reset successfully, which is what distinguishes
"nothing was changed" from "something was changed and then consumed".

`TestPasswordResetIPRateLimit_UnreviewedRoutesSpendNoBudget` was **narrowed**
rather than deleted: `/finalize` left that test's premise, and `/verify` and
`/continuation` remain unreviewed and unbudgeted.

**Both changes were verified to be detected by these tests.** Removing the
`PASSWORD_WORK_UNAVAILABLE` branch fails four of the five unit tests (the
cancellation one correctly still passes, because it asserts behaviour that must
*not* change). Removing the middleware from the route fails all five rate-limit
integration tests. Nothing here is a test that would pass either way.

**Commands run, and their results:**

```sh
go build ./...                                              # exit 0
go vet ./...                                                # exit 0
go test ./internal/api/auth/password_reset/... -v -count=1  # 27/27 pass
go test ./internal/integration/ -run 'PasswordReset|PasswordWork' -v -count=1
                                                            # 39/39 pass
go test ./... -count=1                                      # exit 0, 20 packages ok
go test -race ./... -count=1                                # exit 0, no DATA RACE
```

The race detector **was** run, on the whole module, and passed; this change adds no
concurrent code, and the two concurrency characterization tests from the audit sit
alongside it unchanged. The new tests were additionally run with `-count=12`
(integration) and `-count=5` (unit) with no flakes.

### Swagger

`docs/swagger.json`, `docs/swagger.yaml` and `docs/docs.go` are regenerated from
the updated handler annotations. They are the only generated artefacts touched, and
the regeneration was verified to produce **no unrelated churn**: the diff across
all three is the finalize description plus the two new `429` and `503` responses
(`swagger.json` and `docs.go`: 3 hunks, +13/-1 each; `swagger.yaml`: 4 hunks,
+18/-0).

The generator was run at the version this module pins:

```sh
go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/api/main.go -parseInternal
```

The `@version` suffix matters and resolves outside this module's dependency graph,
so `go.mod` and `go.sum` are unchanged by it — verified by hash before and after.

**A correction to an earlier note in this file's history:** the previously installed
`swag` binary reports itself as `v1.16.4`, and that was previously read as a
version mismatch against the pinned `v1.16.6`. It is not one. swaggo/swag
`v1.16.6` hardcodes `Version = "v1.16.4"` in its own `version.go`, so the two
report the same string and the installed binary's label says nothing about which
source it was built from. Churn had to be measured by generating into a scratch
directory and diffing, which is what was done.

### Still open from the audit

Three findings from the password reset audit were **unresolved**. One has since
been closed and two have not.

- **F3 — no per-address budget on PIN issuance in this feature.** Registration
  pairs its `pin-ip` counter with a per-address `pin-email` budget of five an
  hour; password reset has no second dimension, so `password-reset-pin-ip` is the
  only ceiling on what one address can be mailed. **The per-client-address part of
  F3 is now closed** — `/verify` carries `password-reset-verify-ip`. **The
  per-address part is still open**, and the exposure is stated in full under
  "Cross-code guessing is not solved by this" in the section below, together with
  why a threshold has deliberately not been invented.
- **F4 — `CreatePasswordReset` had no single re-run on
  `pending_password_resets_email_pending_idx`.** **Now closed**: it takes the same
  single gated re-run registration uses, with an integration test that fails
  without it.
- **F5 — no unique index on `password_reset_continuations.pending_password_reset_id`.**
  Registration has `registration_continuations_pending_registration_unique_idx`;
  this feature has only a non-unique index. The flow is currently safe because
  `MarkVerificationRequestVerified` is the single-use authority inside the
  transaction, but it is the only guard of the three that a constraint does not
  also enforce. **Still open**, and it needs a migration plus a separate decision
  about whether the extra constraint is wanted at all.

### Not done here

`GET /auth/password-reset/continuation` remains unbudgeted. It is a pure read, and
registration reaches the same conclusion on its own continuation route.

---

## Password reset verification budget, creation race, and passwordless reset — implemented

Three changes. The first bounds the PIN verification route; the second stops a
double-tapped reset request from being refused; the third closes a coverage gap
that had never been tested end to end.

### A budget for `POST .../verify`

`password-reset-verify-ip`, **20 per 10 minutes** per client address, mounted in
front of the handler.

**The value is registration's `verify-ip` policy, unchanged.** The route does the
same act at the same cost — one verification lookup, one code lookup, one HMAC
comparison, and a counter write on failure — so there is no reason for the two
features to differ, and `registration/constants.go` already carries the reasoning.

**This is a ceiling on cost, not on guessing.** The five-attempt limit per
verification code is untouched and remains the thing that caps guesses; it is
enforced by the database (`GetActiveVerificationCode` refuses to return a code
that has reached the limit, and `IncrementVerificationCodeAttempts` re-checks
`attempts < max` under the row lock). No second, application-level attempt
counter was added. What the new budget bounds is the work a request costs **even
when it guesses wrong**, which nothing else bounded.

The namespace is its own rather than a reuse of registration's `verify-ip`, for
the same reason every other namespace here is separate: a caller who has spent
their password-reset verification allowance has spent nothing of registration's,
and one flow's flood must not lock the other out.

### Cross-code guessing is **not** solved by this, and no per-address budget was added

The five-attempt limit belongs to one `verification_codes` row. Issuing a
replacement code creates a new row whose counter starts at zero, so the limit does
not accumulate across codes. What currently stands between an attacker and a full
sweep of the six-digit space is the combination of:

- `password-reset-verify-ip` — 20 requests per address per 10 minutes. **Per client
  address, not per account**, so it is trivially multiplied by rotating source
  addresses.
- `password-reset-pin-ip` — 20 issuances per client address per 10 minutes.
- The sixty-second per-verification resend cooldown, which is what actually stops
  code re-arming within one reset.
- One open reset per address at a time — `pending_password_resets_email_pending_idx`
  plus the fifteen-minute reset lifetime — so a new reset cannot be opened while
  the old one is live.
- Five attempts per code.

**Password reset has no per-address PIN budget.** Registration pairs its `pin-ip`
counter with a second dimension, `pin-email` at five an hour, charged against the
address inside the service. Password reset has no equivalent, and
`password-reset-pin-ip` is therefore the only ceiling on what one address can be
mailed. That was already recorded as a gap when the IP budgets were added and it
is **still open**.

It is deliberately left open rather than closed here. A per-address issuance
budget is keyed on an address a caller does not control: anyone can request a
reset for someone else's address repeatedly and spend their budget, locking the
real owner out of recovering their own account. That is the same reasoning that
keeps `google-auth-ip` free of a per-email dimension, and it is a product decision
rather than a missing control. Setting a threshold without deciding that trade-off
would implement a denial primitive against account recovery, which is a worse
outcome than the exposure it removes. **The decision is left open, with the
exposure stated above so it can be made deliberately.**

### Two simultaneous first requests now share one reset

`CreatePasswordReset` is now a single re-run gated on one constraint, mirroring
`registration.CreateManualRegistration`.

Neither request can be decided by the lookup inside the transaction, because there
is no row yet for it to lock: both find nothing, both proceed to the insert, and
one loses on `pending_password_resets_email_pending_idx`. The loser is now run
again; by then the winner has committed, the lookup finds its row, and the ordinary
already-pending path produces exactly the answer the second sequential request
would have got.

**The retry condition is exact:** it fires only when `errors.As` recovers a
`*pgconn.PgError` whose `ConstraintName` is `pending_password_resets_email_pending_idx`.
It is a **single** re-run, not a loop. Any other unique violation, any other
database error, and a second loss are all reported as they are. No sleep, no
force-expiration, and no change to the reset-expiration policy. `mapPasswordResetDBError`
is unchanged, so a second loss still reports `PASSWORD_RESET_ALREADY_PENDING`
rather than being swallowed.

### A reset now has end-to-end coverage for an account with no password

A social registration creates no `password_credentials` row, so finalization has
to *create* the credential rather than update it. A mock cannot show that — it
cannot distinguish an upsert that inserted from one that updated, which is the
entire claim — so the new test drives the real HTTP flow against real PostgreSQL
for an account with no credential, from request through to signing in again.

### Verification

**Verify budget** — five integration tests:

- `VerifyRedisFailureFailsClosed` — an unusable limiter gives `503`
  `IP_RATE_LIMIT_UNAVAILABLE` with **no fabricated `Retry-After`**.
- `VerifyExceededCarriesRetryAfter` — walked against **real Redis**, because a
  fake can be told what to report. Asserts exactly three refusals after exactly
  `VerifyIPRateLimitMax` accepted requests, a positive wait never exceeding the
  window, and that a refused request does not extend it.
- `VerifyRefusalSpendsNoCodeAttempt` — the property the budget exists for. Twenty
  wrong guesses within budget move the code's counter to exactly five and no
  further, the next three requests are refused `429`, and the counter is unchanged
  afterwards. The verification is still `pending`, still holds one code, and no
  credential exists.
- `VerifyOneAddressDoesNotSpendAnother` — a neighbour is unaffected.
- `VerifyAndIssuanceAreSeparate` — spending the verify budget leaves the issuance
  budget untouched, so a caller who mistyped a PIN can still ask for a new code.

The wrong guesses in that test are genuine: the code is pinned to a PIN the tests
never submit. An earlier version pinned it to the PIN the requests *did* submit,
which made the first request succeed and consume the code — the test then passed
for the wrong reason and had to be corrected.

**Creation race** — three integration tests, the first using the shared
`runConcurrently` barrier and no sleeps:

- `ConcurrentFirstRequestsShareOneReset` — every request answered `201`, every
  response naming the **same** `verification_id`, exactly one pending reset and
  exactly one verification request, and the shared id provably addressing the row
  that exists. It then issues a PIN against it, so the id is shown to be usable
  rather than merely equal.
- `SecondRequestReusesTheOpenReset` — the sequential already-pending path is
  unchanged, which is the path the retry's second pass runs on.
- `UnknownAddressStillWritesNothing` — concurrent requests for an address no
  account holds still answer `201`, still hand out **distinct** placeholders, and
  still write nothing. The retry must not have turned the non-enumerating path
  into one that creates rows.

**The participant count is 64, and that is a measured choice.** The window between
the locking SELECT returning "no rows" and the INSERT is microseconds, while each
request spends far longer before it. With two contenders one usually commits first
and the loser is absorbed by the ordinary already-pending path, so **a
two-participant version of this test passed with the retry removed and proved
nothing**. Measured on this setup with the retry removed: 16 contenders produce
four or five conflicts every run, 32 produce six or seven, and 64 produce seven on
every run across repeated trials. Sixty-four is used so the race is entered
reliably rather than usually. These requests use the suite's permissive app so
arranging the contention never spends a budget under test.

**Both changes were verified to be detected by their tests.** Removing the retry
fails `ConcurrentFirstRequestsShareOneReset` on **three runs out of three**.
Removing the middleware from the verify route fails the verify budget tests.

**Passwordless reset** — `EndToEndForAccountWithoutCredential` asserts from HTTP
and PostgreSQL that the account starts with no credential, that the continuation
route reports `has_password_credential == false` **before** finalization and
consumes nothing, that finalization succeeds, that exactly one credential exists
afterwards, that it is not the plaintext and verifies against the new password,
that the reset is `completed` and the continuation consumed, that both seeded
sessions are revoked, that **signing in with the new password works**, and that
replaying the continuation is refused. The sign-in is the only external proof that
the row written is a real credential rather than a well-formed one.
`ContinuationReportsCredentialPresenceAccurately` covers both account shapes
side by side; the existing end-to-end test for an account that already has a
credential is untouched.

No production defect surfaced from the passwordless case. The upsert behaves as
designed and was not changed.

### Swagger

`docs/swagger.json`, `docs/swagger.yaml` and `docs/docs.go` are regenerated. The
diff is confined to the verify route: its description plus the new `429` and `503`
responses (`swagger.json` and `docs.go`: 3 hunks, +13/-1 each; `swagger.yaml`:
4 hunks, +16/-0). Nothing else in the document moved.

```sh
go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/api/main.go -parseInternal
```

The generator was measured against a scratch output directory before being pointed
at `docs/`, so the churn figure above is observed rather than assumed. `go.mod` and
`go.sum` are unchanged by it.

### Commands run, and their results

```sh
go build ./...                                              # exit 0
go vet ./...                                                # exit 0
go test ./internal/api/auth/password_reset/... -count=1     # all pass
go test ./internal/integration/ -run 'TestRequestPasswordReset_' -count=3
                                                            # pass
go test ./internal/integration/ -run 'TestPasswordResetIPRateLimit_Verify' -count=1
                                                            # 5/5 pass
go test ./internal/integration/ -run 'PasswordReset|PasswordWork' -count=1
                                                            # all pass
go test ./... -count=1                                      # exit 0
go test -race ./... -count=1                                # exit 0, no DATA RACE
```

---

## Per-user budgets on the saved-item routes — implemented

Two of the four routes in `internal/api/saved_item` and
`internal/api/enrichment` now carry a budget keyed on the authenticated user
rather than on a client address. Both are mounted **after `middleware.Auth`** and
**before the handler**, in the order that matters: the identity is what is being
charged, and a refused request must never reach the work it guards.

| Route | Namespace | Policy |
| --- | --- | --- |
| `POST /saved-items` | `saved-item-create-user` | 60 per 10 minutes |
| `POST /saved-items/:id/enrich` | `saved-item-enrich-user` | 10 per minute |

### Why the user and not the address

Every rate limit in this application before these two is keyed on a client
address, because every one guards a route reachable **without authentication**,
where an address is the only thing a caller has. These two routes are different:
they act on rows owned by exactly one account, and every query they run is
already scoped by that account.

- An address budget does not survive a caller changing address, so it is not a
  ceiling on one account at all — it is a ceiling on one network path.
- It charges a household or an office behind one NAT for each other's activity,
  which for `/enrich` would mean one person fixing several items could exhaust a
  colleague's allowance.
- The identity is **already verified** by `middleware.Auth` and is the thing
  ownership is checked against, so it costs nothing to use and cannot be forged.

`security.AccessTokenClaims.Subject` is used directly. It is a required claim —
`AccessTokenVerifier` rejects a token without one — so the subject is never
invented, empty, or attacker-chosen.

### Why these two routes and no others

A budget is only worth having where it bounds something shared.

- `POST /saved-items/:id/enrich` is **the only route in the application that
  performs an outbound fetch inside the API process**. Each call holds a handler
  and a database connection for as long as `security.OutboundFetchTimeout`
  permits. Every other page read happens in the worker, where
  `WorkerConcurrency` already bounds it. That makes this the tightest budget in
  the application: the scarce resource is shared by every account.
- `POST /saved-items` is cheap in itself — a lookup, one insert, one queue write
  — but it is what **arranges** unbounded work: every save enqueues an
  enrichment task, the producer sets no `asynq.Unique` or `TaskID`, and the queue
  is not capped. What the budget bounds is queue growth, not the insert.
- `GET /saved-items/:id` and `DELETE /saved-items/:id` are deliberately **not**
  limited. Neither fetches, enqueues, nor writes enrichment state; they cost what
  every other authenticated read costs. A limit there would bound nothing.
- **Collection and search routes are untouched.** They hold no cursor or lock that
  outlives a request, perform no outbound fetch, and enqueue nothing.

**These thresholds are initial engineering estimates, not production-validated
values.** No deployment has been selected and no traffic observed. Ten a minute
for `/enrich` sits where a person cannot reach it by re-enriching deliberately
while a genuine user fixing several items in a row still fits; sixty in ten
minutes for a save is well above ordinary use. Revisit both against real traffic.

### Separate namespaces, deliberately

The two routes cost different things, so they must not share one allowance: a
user who has been throttled for saving must still be able to refresh an item
whose page looks wrong, and the reverse. One shared counter would let a caller
saturating the cheap route starve the expensive one. Both are also separate from
every auth namespace — reaching either route is not an authentication attempt,
and an exhausted save budget must never lock anyone out of signing in.

### Redis failures — fail closed, on both routes

An unreachable counter is **not** permission. Letting a request through would let
anyone remove the ceiling by causing the failure that produces it, so both routes
answer `503 USER_RATE_LIMIT_UNAVAILABLE` with **no `Retry-After`** (there is no
known recovery time) and perform no work: a refused save writes no row and
enqueues nothing, a refused enrichment performs no fetch and writes no
enrichment state.

**The save's fail-closed policy is a choice, not the only defensible one, and is
recorded as such.** The enrichment route has no real alternative — it spends an
outbound fetch inline and that is precisely the work the budget exists to bound.
The save could honestly have been treated as cheap enough to accept blind. It was
not, because a save schedules a task on a queue nobody can currently measure, and
accepting writes while blind to the queue is how that queue grows unbounded
exactly when the process is already in trouble.

### Error codes

`USER_RATE_LIMIT_EXCEEDED` (429, with `Retry-After`) and
`USER_RATE_LIMIT_UNAVAILABLE` (503) are declared in `internal/middleware` beside
the middleware, alongside the existing `IP_RATE_LIMIT_*` pair. They name the
**dimension**, not the endpoint — the path already says which route refused, and
the namespace is what says which operation — which is the convention
`middleware.IPRateLimit` already followed.

A request that reaches the limiter with no verified claims is a `500`, not a 429
and not an invented subject. It is unreachable when the middleware is mounted
after `Auth`, so reaching it means a wiring mistake; merging every unidentifiable
request into one counter would hand a caller the choice of which budget it lands
in.

### Wiring

`saveditem.RegisterModule` and `enrichment.RegisterModule` each take one
`middleware.RateLimiter`. `cmd/api/main.go` passes the **same**
`pinRateLimiter` instance it already builds for the auth budgets — one limiter
serves every budget in the process, and a limiter per feature would make the
total the sum of the parts. `collection.RegisterModule` is unchanged.

`internal/testutil` gained `NewAppWithEnqueuerAndLimiter`, because the existing
constructors could supply an enricher or a limiter but not both. One limiter is
still injected for the whole app, as production does it, so cross-namespace
isolation stays testable.

### Verification

**Middleware unit tests** — `internal/middleware/user_ratelimit_test.go`, ten
tests: the subject is the token's subject and nothing else; two accounts are two
budgets; `429` carries a whole-second `Retry-After` (rounded up, never negative,
fractional waits covered); `503` fabricates no wait; a refusal does not reach the
handler; an unauthenticated request is refused by `Auth` with the limiter **never
asked**; missing claims is a `500` and never becomes a subject; and mounting the
limiter *before* `Auth` is asserted to break the route rather than merely leave it
unbounded.

**Integration tests** — `internal/integration/saved_item_user_rate_limit_test.go`,
nine tests driving the real app, handlers, services, repositories and PostgreSQL,
with only the limiter substituted:

- `WithinBudgetChargesTheCallersAccount` / `WithinBudgetFetches` — assert the
  namespace, the subject **and the exact policy**, so a route consulting a
  limiter under the right name with a different allowance cannot pass.
- `ExceededCreatesNothingAndQueuesNothing` — `429` with `Retry-After`, zero rows,
  zero queued tasks.
- `ExceededDoesNotFetchAndWritesNothing` — `429`, the enricher was never called,
  and `enrichment_status` is still `pending` with no `last_enriched_at`.
- `TheTwoBudgetsAreIndependent` — exhausting either leaves the other whole.
- `OneAccountDoesNotSpendAnother` — a full 60-request flood from one account, then
  a **neighbour** still gets `201`.
- `UnreachableCounterFailsClosed` — both routes, with no side effect in either
  case.
- `UnauthenticatedIsRefusedBeforeTheBudget` — `401` on both routes, with no
  account created and an enrichment id that does not exist, so the answer is
  shown not to depend on the row situation.
- `ReadsAndDeletesAreNotLimited` — a limiter that refuses **everything** is wired
  in and both limited routes still work, while `GET` and `DELETE` consult neither
  namespace. This is what pins the deliberate scope.

**These tests were verified to detect the limiter's absence.** Removing the
middleware from both routes fails **7 of the 9** integration tests. The two that
still pass are the negative tests by design —
`UnauthenticatedIsRefusedBeforeTheBudget` and `ReadsAndDeletesAreNotLimited`
assert what must *not* happen, which is true with or without the middleware.
Nothing in the file is a test that would pass either way about the limiter's
presence.

The new tests were additionally run at `-count=3` (integration) and `-count=5`
(middleware) with no flakes.

### Swagger

`docs/swagger.json`, `docs/swagger.yaml` and `docs/docs.go` are regenerated from
the updated handler annotations. The diff is confined to the two limited routes:
each gains a description line, two error codes in its description list, and `429`
and `503` responses. **No other route in the document moved** — verified by
reading the full diff, not by trusting the generator.

```sh
go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/api/main.go -parseInternal
```

### Still open

- **Failing closed on `POST /saved-items` is a deliberate choice** recorded above,
  not a settled decision. It is the one policy here with a reasonable
  alternative, and which is right depends on how the save path is expected to
  behave during a Redis outage.
- **No per-item or per-URL budget was added.** Two accounts hitting `/enrich`
  about the same URL are both charged, and the worker can still be asked to fetch
  the same page repeatedly. Adding one needs a decision about whether duplicate
  enrichment is acceptable product behaviour, which it may well be.
- **The thresholds are unmeasured**, as stated above.

### Commands run, and their results

```sh
go build ./...                                              # exit 0
go vet ./...                                                # exit 0
go test ./internal/middleware/ -run TestUserRateLimit -count=5
                                                             # pass
go test ./internal/integration/ -run 'TestSavedItemCreateUserRateLimit|TestSavedItemEnrichUserRateLimit|TestSavedItemUserRateLimit' -count=3
                                                             # pass
go test ./... -count=1                                      # exit 0, 20 packages ok
go test -race ./... -count=1                                # exit 0, no DATA RACE
```

The race detector **was** run on the whole module and passed.

---

## Outstanding deployment work — not implemented

These are unresolved because the hosting platform and the email provider have
**not been selected**. They are deployment tasks, not application gaps, and the
distinction matters: the mechanisms below exist and are verified, and what is
missing is the information only a chosen environment can supply.

### 1. Client IP and deployment proxy configuration

- Select the actual hosting platform.
- Determine whether the application is behind a reverse proxy or load balancer.
- **Verify the actual forwarding-header behaviour** by sending a forged
  `X-Forwarded-For` to a deployed instance and observing what the application
  resolves. Whether a platform appends to or replaces the header determines
  whether a trusted-proxy configuration is correct at all.
- Configure `CLIENT_IP_SOURCE` and `TRUSTED_PROXIES` from verified information
  only.
- Test real client-address resolution in the chosen environment.
- Reassess the initial per-IP threshold against real traffic and against
  shared-address false positives. There are now **three** per-IP policies, not
  one: `20/10min` for PIN issuance, `30/10min` for manual login and
  `60/10min` for Google authentication. They are separate counters and one being
  spent says nothing about the other, so all three need a traffic-informed
  threshold.

**The specific risk that cannot be closed in code.** With the default `peer`
source, a deployment that is actually behind a proxy resolves every client to the
proxy's own address, putting them all in one counter. That is a misconfiguration
with a visible symptom — unrelated users being refused — rather than a silent
one, and the startup log records the resolved mode so a deployment's actual
configuration is in the record. It is not eliminated, only made visible.

Two things make it harder to leave unnoticed and are worth knowing:

- `app.Test` (Fiber v3.5.0) serves every request from an in-memory connection
  whose `RemoteAddr()` is hard-coded to `0.0.0.0`. Per-address behaviour cannot
  be exercised through it without a configured trusted proxy, which is why the
  integration tests drive client addresses through a proxy-configured app.
- Fiber's `isValidProxyIP` classifies a dotted-quad-in-IPv6 form such as
  `::ffff:198.51.100.5` as neither a valid IPv4 nor a valid IPv6 address, so the
  walk skips it and falls back to the peer address. Such a client is grouped with
  the proxy rather than separated. This errs toward over-sharing, never toward
  evasion, and `NormalizeIP` handles the form correctly when it reaches it.

**Login makes the consequences of that misconfiguration worse than PIN did.** A
collapsed counter on `POST /auth/login` means one unlucky address exhausts a
30-request budget that everyone behind the proxy shares, and the next real user
cannot sign in at all. The per-IP budget there is deliberately set higher than
the PIN one for exactly this reason, but it is mitigation, not a fix.

The same misconfiguration now affects Google authentication too, and that one is
worth watching separately: a collapsed `google-auth-ip` counter would refuse
Google sign-in for everyone behind the proxy. It is set to 60/10min — twice the
manual-login budget — because a Google refusal is never a guess running out of
attempts, so every refusal it produces is a false positive rather than a useful
one. It is mitigation, not a fix, and the same `CLIENT_IP_SOURCE` verification
applies.

### 2. Email provider selection and production configuration

- Select the email delivery provider. None has been chosen; non-development
  builds currently use `email.NewUnconfiguredSender()`, which errors on send.
- Document its required environment variables and secret management.
- Complete sender/domain verification and any provider-required IP
  verification or allowlisting.
- Document provider-specific errors, delivery failures, quotas and retry
  behaviour.
- Configure provider-level sending limits, usage monitoring and budget
  safeguards where available.
- Verify that the application's rate limits complement rather than replace
  provider-level safeguards.

**Consequence of the second one, recorded here so it is not lost:** until a
provider exists, the per-email budget is the _only_ ceiling on what verification
mail this application can trigger. It is a real control and it is verified, but
it is an application-level one, not a provider-level one.

### 3. Production readiness

- Verify Redis availability, TLS and connection configuration, and failure
  behaviour for the selected hosting platform. The limiter fails closed, so a
  Redis outage currently stops PIN issuance, **manual login and Google
  authentication** entirely rather than letting any of them through unthrottled —
  deliberate, and worth confirming is the intended availability trade-off for the
  chosen environment. Manual login is the sharper half of that trade: an outage
  means nobody can sign in, and unlike PIN issuance there is no second way in for
  the user to fall back on. Google authentication is now the same: it is one of
  only two doors in, and it is blocked by the same outage for the same reason.
- Reassess both the per-email and per-IP policies after observing realistic usage.
  For login specifically, `LoginEmailFailureLimit`/`LoginEmailFailureWindow` in
  `internal/api/auth/login/constants.go` is the single value to revisit if
  account lockouts are reported — an attacker who knows an address can
  deliberately exhaust that budget and lock the account out for up to the window.
  The window is kept short precisely so recovery is quick.
- Confirm the Argon2id active parameters (`m = 64 MiB`, `t = 3`, `p = 4`) are
  right for the chosen instance size, and that `argon2MaxMemory` (128 MiB) still
  leaves a verification survivable when several run at once. The ceilings exist
  so a stored hash cannot ask for unbounded work; the active parameters are what
  a legitimate verification actually costs.
- Confirm `PASSWORD_WORK_CONCURRENCY` against the chosen instance. The default
  of `1` suits a constrained instance, where a single derivation is already a
  large share of the memory available. At one vCPU a measurement on the target
  showed two concurrent derivations taking twice as long as one — fully
  serialized — while holding twice the memory, so on that shape the limit costs
  nothing to keep at one. A deployment with memory to spare should raise it and
  measure rather than take the default on trust.
- Ensure deployment documentation does not imply that the email provider or the
  hosting platform has already been selected. Neither has.

---

## Testing and verification status

The current tree has been verified with:

- `go build ./...` — pass
- `go vet ./...` — pass
- `go test ./... -count=1` — pass
- `go test -race ./... -count=1` — pass, 20 packages, no data race reported
- `sqlc generate` — pass
- `mockgen` — pass
- `swag init -g cmd/api/main.go -parseInternal` — pass
- `gofmt -l` on every file changed — clean

The generators were re-run after the change and produced byte-identical output,
so no generated file is stale.

Integration tests require Docker and use the project's PostgreSQL test container.
The queue-facing integration tests additionally start a Redis testcontainer,
lazily and only when one is needed. The rate-limit integration tests use that
same lazily-started Redis rather than a second one.

The **PIN** rate-limit and concurrency tests were additionally re-run
repeatedly (`-count=6` for the integration ones, `-count=8` for the unit ones)
and showed no flakes. That repeated running was done when the PIN budgets were
added and has not been repeated since.

**The login rate-limit tests have not been run repeatedly.** They are recorded
here as single `-count=1` runs only, so no flake-resistance claim is made for
them. The one login test that is timing-sensitive — the enumeration floor in
`internal/api/auth/login` — is no longer the sole guard for that property: it is
now backed by the four deterministic tests described above, and it was itself
re-run under `-race` at `-count=10` clean and at `-count=8` clean with a full
`-race` suite running concurrently, which is the condition that used to break
it. The remaining manual-login rate-limit tests are still single-run, so no
claim is made for those.

**The Google authentication rate-limit tests have been re-run**, and the
Google-specific ones only: `internal/integration` with `-run
TestGoogleAuthIPRateLimit` at `-count=4` under `-race` showed no flakes, and the
new route tests in `internal/api/auth/login` at `-count=6` likewise. The
manual-login rate-limit tests remain at the single-run status described above, so
no claim is made for those.

**The rate-limit tests are proven to detect what they claim to.** Unmounting
`googleAuthIPLimit` from the `GET .../account-link/:confirmation_id` route makes
both the unit tests and
`TestGoogleAuthIPRateLimit_AllThreeRoutesSpendOneBudget` fail, and the mount was
restored afterwards. This mirrors the existing precedent for the manual-login
concurrency and enumeration tests recorded above.

**The race detector has been run, locally and in full.** `-race` requires cgo, so
it needs a C compiler; GCC 16.2.0 (MSYS2 UCRT64) is now installed on the Windows
development machine and the whole suite runs under it:

- `go test -race ./internal/security/... -count=1` — pass
- `go test -race ./internal/api/auth/login/... -count=1` — pass
- `go test -race ./internal/api/auth/registration/... -count=1` — pass
- `go test -race ./internal/integration/... -count=1` — pass, no `DATA RACE` reports
- `go test -race ./internal/ratelimit/... ./internal/middleware/... -count=1` — pass
- `go test -race ./... -count=1` — pass, all 20 packages, no `DATA RACE` reports

The limiter and hasher concurrency tests were additionally re-run five times
each under the detector, since a single run is weak evidence where the code is
contended by design.

**GCC visibility must be checked at the start of a session, not assumed.**
`C:\msys64\ucrt64\bin` is now permanently on the Windows **User** `Path` and
`CGO_ENABLED=1`, so a newly launched terminal resolves `gcc` normally. A process
that was already running when the `Path` changed keeps the `Path` it was started
with, so `go test -race` fails with `cgo: C compiler "gcc" not found` even
though the machine is correctly configured. This happened in exactly that way
once. **Before running race tests in a new session, check `where.exe gcc`
resolves and `go env CGO_ENABLED` returns `1`.** If it does not, prepend the
directory to that shell's `Path` for the command rather than changing project
configuration:

```powershell
$env:Path = 'C:\msys64\ucrt64\bin;' + $env:Path
```

The full `-race` suite was run **twice** after the login timing fix, both clean
across all 20 packages, and the login enumeration floor was re-run repeatedly
under `-race` both quietly and while a full `-race` suite ran concurrently.

**CI still does not run it.** `.github/workflows/test.yml` runs
`go test ./... -v` on `ubuntu-latest` without `-race`, so the verification above
is local to a machine with a C compiler and is not reproduced on every change.
Adding `-race` to that step needs no further configuration on `ubuntu-latest` and
is outstanding.

The enrichment foundation was also separately verified with:

- `internal/security` — 100% statement coverage
- `internal/enrichment` — 95.4% statement coverage

The worker's unit-level packages are verified by unit tests plus the integration
suite above, which exercises their real behavior against Postgres and Redis.
Statement coverage figures for `internal/worker/*` are not recorded here, since
their meaningful behavior is in the integration path rather than in isolated
calls.

The worker binary's Redis connectivity and startup have been verified, and the
full background path is covered by the integration tests above. Those tests replace
only the extraction layer, for the same reason the synchronous enrichment tests do
(the guarded outbound client correctly refuses loopback); the real extractor has
been verified against live external origins by manual walkthrough instead.

---

## Important Decisions

Do not change these casually. Revisit only with an explicit decision.

1. **`saved_items.platform` is the content/source platform, not the client
   platform.** It represents services such as YouTube or Spotify, not web,
   Android, or iOS.

2. **`X-Platform` is session-only.** It populates `sessions.platform` and must
   never be used by Saved Item enrichment.

3. **`platform` is metadata-derived.** It must not be inferred from the URL
   hostname, client input, or an implicit domain-to-platform mapping.

4. **`domain` is derived and normalized server-side.** Only lowercase and one
   leading `www.` are removed. Do not add public-suffix or registrable-domain
   logic without an explicit decision.

5. **Duplicate URLs are allowed.** There is no unique constraint, deduplication,
   or `url_hash`.

6. **No Saved Item update/PATCH endpoint exists yet.** Do not add one unless
   explicitly requested.

7. **Delete is a hard delete.** There is no soft-delete or `deleted_at`.

8. **`POST /saved-items` enqueues enrichment, and the synchronous endpoint
   remains available.** A save commits its row first and then queues the item's
   id. `POST /saved-items/:id/enrich` still enriches one specific item on
   demand, and enrichment is repeatable from either path.

   Both paths produce the same row for the same page, and neither changes
   `collection_id` or `domain`.

9. **Enrichment has exactly three persisted states: `pending`, `completed`,
   `failed`.** There is deliberately no `processing` or `retrying` value. How
   many more attempts the queue will make is execution mechanics, not the state
   of the Saved Item, so it is never written. Adding a status value requires an
   explicit decision and a migration.

10. **The guarded HTTP client is the only outbound fetch path for user-supplied
    URLs.** Do not add another client, transport, or SSRF implementation. The
    worker constructs the same one and shares no other outbound path.

11. **Outbound ports are restricted to 80/443.** Widening this allowlist requires
    an explicit decision.

12. **`internal/enrichment` is reusable extraction logic, not persistence.**
    It has no Fiber, SQLC, Asynq, or database responsibility. It is shared by
    the synchronous endpoint and the worker, which is why neither path can
    diverge on what a page error means.

13. **Enrichment must not modify `collection_id`.** Metadata enrichment and
    automatic collection organization are separate concerns. Organization writes
    `collection_id` and nothing else.

13a. **Automatic organization is event-driven and never overrides a user.** A task
exists only because a successful enrichment produced a platform, and it may only
move an item that is still in `Unsorted`. Do not add a scan, a sweep, a periodic
query, or a persisted organization status to find items the event missed.

13b. **`collections.type = 'system'` describes who created a collection, not who
owns it.** A system collection belongs to exactly one user through
`collections.user_id`, and migration 000025's composite foreign key is what makes
an item's collection and its owner have to agree.

14. **The worker has a separate configuration boundary.** It uses
    `WorkerConfig` / `LoadWorker` and does not depend on API-only secrets. It also
    imports nothing from `internal/api`.

15. **`FailureKind` separates a page that cannot be used from an origin that is
    not answering.** Terminal client statuses, non-HTML content and unparseable
    documents are permanent; network failures, unreadable bodies, `429` and `5xx`
    are retryable within the bounded budget. Changing which group a failure falls
    into changes retry behavior, so treat it as a product decision.

16. **A save succeeds even when enqueueing fails.** A failed enqueue leaves the
    item `pending`, and there is no reconciliation pass for it either. This is a
    deliberate trade-off: the Saved Item is valid product data regardless of its
    metadata, and the user can always ask on demand.

17. **Canonical URLs are not automatically applied.** The extractor may report a
    canonical URL, but replacing the Saved Item's original URL is a separate
    product decision.

18. **`Metadata.Author` and `Metadata.SiteName` are currently extraction-only
    fields.** Adding persistence for them requires an explicit product/schema
    decision.

19. **Deleting a saved item never deletes its collection.** The delete reports the
    collection, whether it is now empty, and whether the user may delete it, and
    those are advisory values for the caller. Removing a collection is a separate
    endpoint the caller invokes deliberately. An empty collection is a valid state,
    so removing one as a side effect would take an action nobody asked for.

19a. **Deleting a collection is an explicit destructive operation whose caller
states the disposition of its saved items.** `saved_items_action` is `delete` or
`move` with no default, because the two have opposite consequences and one
destroys content. Nothing is inferred on the caller's behalf, and a missing
target is an error rather than a substitution.

19b. **Unsorted is identified by `system_key` and is the only protected
collection.** Collection type is not the criterion: `type = 'system'` says who
created a collection, not what it is, so an automatically created collection is
deletable like one the user named. Unsorted as a _move target_ is ordinary and is
named by its id; there is no `move_to_unsorted` mode and no fallback to it.

19c. **`saved_items_collection_id_fkey` remains the final guard on collection
deletion.** The children are disposed of first because `collection_id` is
`NOT NULL`, and the FK decides emptiness at the final DELETE rather than an
application pre-check. A violation rolls the transaction back and is reported as
`COLLECTION_NOT_EMPTY`. Never change it to `ON DELETE CASCADE`.

19d. **The move-all-items behavior exists only as part of collection deletion.**
`MoveSavedItemsToCollection` has no endpoint of its own. A general batch-move
feature is a separate product decision and is deliberately not implemented here.

19e. **A cursor is a position, not a row.** It records where a page ended, and the
next query resumes from there without ever reading the row it came from. So a
cursor keeps working after that row is deleted or renamed, and a cursor pointing
past the end returns an empty page rather than an error. This is the property that
separates this from offset pagination, where a deleted row shifts everything after
it and the client silently receives a duplicate.

19f. **`GET /collections` uses cursor pagination; the existing offset list endpoints
are unchanged.** `GET /collections/:id/saved-items` is cursor-paginated too.
`GET /auth/sessions` keeps `page`/`limit` and `meta.pagination`. `httpx.Meta`
carries both, with `cursor` omitted entirely where unset, so no existing response
changes. Converting the session endpoint is a separate piece of work and needs its
own decision.

19g. **The Inbox is a cross-collection recent history, served by
`GET /saved-items`. Every saved item belongs to exactly one collection, so each
item reports that collection and the history is never confused with any single
collection's contents.** This supersedes the earlier decision that there is no
standalone saved-item listing. The reasoning it was based on still stands and is
why the endpoint looks the way it does: it reports the full thirteen-field item
including `enrichment_status`, `last_enriched_at`, `description` and
`image_url`, so a caller can tell a page with no title from one that had not
been read, and it carries a nested `collection` of `id` and `name` per item
because a listing that spans collections cannot name one collection for the
whole response. What changed is the product's need: the Flutter Inbox is a
chronological record of what was saved, so an item has to stay in it after it
has been filed into a collection of the user's own.

`GET /collections/:id/saved-items` is unchanged and still the way to see one
collection's contents. `Unsorted` is **not** the Inbox: it is one collection
among the ones an item can be in, it is pinned first in `GET /collections`
because every save lands there, and it is never used as a substitute for the
cross-collection history. Do not "simplify" one of these three into another.

Nothing is filtered out of the history. A `pending` item, an item whose page
could not be read, and an item automatic organization filed are all ordinary
states of a saved item and all appear; a history that hid the two it had not
finished processing would be a record of only what went well. The listing also
never writes and never triggers enrichment or organization.

19j. **A collection listing reports a bounded saved item count, and the bound is
one number with a separate boolean rather than a value a client has to
interpret.** `saved_items_count` is capped at
`CollectionSavedItemsCountCap` (99) and `saved_items_count_overflow` says
whether the real count was higher, so a client renders an exact badge up to 99
and `99+` above it without ever comparing against a threshold of its own. The
count is of the **whole collection**, on **every page** — a count that described
the page would be a different number on each one, and a badge that changed as
the user scrolled would not be a count of anything.

The bound is passed to every listing statement as a query parameter rather than
written into the SQL, so `CollectionSavedItemsCountCap` is the only place it is
stated; a literal in each of the nine statements would be nine places that could
disagree. This is about the size of a collection, not about the size of a page,
and does not reintroduce a pagination `total`: `has_more` still comes from one
extra fetched row.

The bound is on the **rows read**, not only on the number reported. The inner
`SELECT` asks for at most `CollectionSavedItemsCountCap + 1` matching rows and
stops there, so one collection's count costs the same whether it holds 5 items or
50,000. Clipping `COUNT(*)` alone would have bounded only the answer and left the
aggregate walking every row. The `+ 1` is what makes the threshold decidable:
exactly `cap` rows is still an exact count, so without it a collection holding
exactly 99 would be indistinguishable from one holding more.

19h. **A collection listing names its collection.** `GET /collections/:id/saved-items`
reports `data.collection` with `id` and `name`, reused from the ownership lookup
that call already had to make — no extra query. It is reported on every response
including an empty page, because "nothing in it" and "no such collection" are
different things and the name is what tells them apart. `type` and `system_key` are
left out: they answer questions for `GET /collections`, and a client that reached
this endpoint has already named the collection it wants. Unsorted goes through the
same type as everything else.

19i. **A response schema that omits `meta` is a documentation bug, and it is guarded.**
Both cursor endpoints described `meta.cursor` in prose while their generated 200
schema showed a two-key envelope, because `httpx.OKWithMeta` builds `meta` and the
API response structs did not declare it. Nothing at runtime could notice, and the
result is still valid OpenAPI, so no linter did either. Both now declare a
documentation-only mirror sharing one `CursorPageMeta`, and
`TestCollectionAPI_ListSavedItems_DocumentedShape` walks `docs/swagger.json` and a
real body side by side at every level, failing if either gains or loses a key.

19k. **A collection name is a destination, not a reserved namespace.** Filing a
saved item resolves the name against every collection the user owns, and collection
type does not restrict the result. A name already held by a system collection such
as `Unsorted` or `YouTube` resolves to that collection; it is not a name to reject.
`collections_user_name_unique` is per-user uniqueness across both types, not a
reserved-name rule, and `COLLECTION_NAME_RESERVED` is not part of the collection API
contract. The automatic organization worker has its own, narrower reserved-name rule
for platform names and is a separate writer with separate behavior.

20. **A forward header is believed only behind a verified allowlist, and the
    declaration of where a client's address comes from is one setting rather than
    two.** `CLIENT_IP_SOURCE` is `peer` or `proxy`, and everything else — the
    framework's proxy trust, its allowlist, which header is read, and whether the
    forwarded chain is parsed and validated — is derived from it. There is no
    combination in which a header is read without a verified allowlist beside it,
    which is what makes "the header was present" and "the header was believed"
    the same thing. Do not read `X-Forwarded-For`, `X-Real-IP` or any similar
    header outside `middleware.IPRateLimit`'s use of `c.IP()`, and never as an
    identity, a rate-limit subject, or a logging key on its own.

21. **`EnableIPValidation` is mandatory whenever a header is read.** Fiber v3.5.0's
    `extractIPFromHeader` returns the header's raw bytes when it is off, so a
    chain such as `203.0.113.7, 198.51.100.4` would become the client's identity
    — a separate counter for every chain a client chose to send. Turning it off
    next to proxy trust is a vulnerability, not a performance setting.

22. **A limiter that cannot answer refuses; it never allows.** A Redis outage, an
    unusable policy, or a client address that cannot be resolved all produce 503
    rather than a pass. Allowing on failure would let anyone remove the ceiling by
    causing one. Do not add a fallback that admits the request when the counter
    is unreachable.

23. **The two PIN budgets are separate dimensions and neither replaces the other.**
    The per-email budget stops one address being mailed repeatedly, which an
    attacker with many addresses defeats; the per-IP budget stops one address
    reaching the endpoints repeatedly, which an attacker with many proxies
    defeats. Merging them would leave a way in. `pin_issued_count` remains
    issuance history and is not a counter for either.

24. **The IP budget runs in front of the handler, not in the service.** A service
    never sees a request that failed to parse its UUID or named a verification
    that does not exist, so a limit enforced there would count only well-formed
    requests and would be free to burn with the cheapest kind of request there
    is. Moving it into the service would silently weaken it.

25. **`Retry-After` is the counter's remaining window, never the configured one.**
    It is read by the same atomic script that increments the counter, rounded up,
    and never negative. A refused request must not extend the window, and a 503
    must carry no `Retry-After` at all — there is no known recovery time and a
    fabricated one would be a guess presented as a fact.

26. **`verify-ip` and `finalize-ip` are budgets on cost, and each is separate from
    every other flow's.** Verification sends nothing and is bounded because a
    wrong attempt still costs lookups and a write; finalization is the only route
    in the flow that performs an Argon2id derivation, and the shared
    `PasswordWorkLimiter` bounds concurrency rather than rate, so it cannot stop
    one caller from occupying every slot indefinitely. Neither reuses `pin-ip`,
    and neither shares a counter with the other: adjacent steps of one
    registration are two acts, and a user who mistyped a PIN must still be able
    to finalize. Do not merge these namespaces, and do not add a per-email budget
    to either — the email is not known before the expensive work, so the key
    would be attacker-chosen and would let one caller lock a legitimate
    registrant out of their own registration.

27. **`GetVerification` and `GetRegistrationContinuation` stay unlimited.** Both
    are pure reads, so a limit there would cost a polling client its budget
    without bounding any expensive work. `GetVerification` is specifically what a
    client calls to learn when it may resend; refusing it would break the flow
    rather than protect it. Adding a budget to either needs an explicit decision.

    For the continuation route specifically: each request costs a token hash and
    one indexed lookup on `registration_continuations.token_hash`, served by the
    unique index migration 000027 added. It opens no transaction, writes nothing,
    sends no message, makes no external call and performs no Argon2id work. The
    token is high-entropy and is issued only after a PIN has been verified, so
    whoever holds one has already proved control of the mailbox the response
    describes; the response is that caller's own registration metadata, and
    repeating a non-mutating read neither widens nor changes it. The client needs
    to read it while it works out which fields finalization requires.

    `TestRegistrationIPRateLimit_UnlimitedReadRoutesAreNotCounted` pins this by
    asserting the route consults no budget at all. Mounting a limiter here would
    fail that test, which is the point: the decision is recorded, not merely
    unmade.

28. **The single-use transitions are guarded redundantly, and the tests assert the
    outcome rather than a particular guard.** `CompleteVerification` checks both
    `consumed_at IS NULL` and `status = 'pending'` with `rowsAffected == 1`, and
    the continuation is additionally protected by a unique index on
    `pending_registration_id`; finalization is protected by the users insert losing
    `users_email_unique`. Removing any one guard leaves the observable outcome
    unchanged, because a losing request is usually refused at the service's first
    read before the transaction opens. That redundancy is deliberate. Do not
    remove a guard on the grounds that another remains — and do not expect the
    concurrency tests to fail if one is removed, since they assert exactly one
    continuation and exactly one user rather than which guard produced them.

28a. **`POST /auth/register/finalize/social` carries no IP budget, and that is a
    decision rather than an omission.** This route does real work — it creates
    the user, the Unsorted collection and the auth identity, consumes the
    continuation, completes the registration and mints a session — so it must not
    be described as a cheap read. What bounds it is the lifecycle around it.

    It performs no Argon2id derivation, sends no message and does not re-verify
    the Google token, so the `PasswordWorkLimiter` and the reasoning behind
    `finalize-ip` do not reach it. `finalize-ip` exists because manual
    finalization is the one route in the flow that can occupy every password-work
    slot indefinitely; this route never calls the hasher, so that cost is not one
    it can impose.

    The continuation is single-use. `ConsumeRegistrationContinuation` matches only
    while `consumed_at IS NULL AND expires_at > NOW()` and its result is checked,
    with `users_email_unique` and `auth_identities_provider_subject_unique` behind
    it, so one token produces at most one account and one session however many
    times it is presented.
    `TestFinalizeSocialRegistration_ConcurrentSameTokenCreatesExactlyOneAccount`
    drives concurrent requests with a single token and asserts exactly that.

    A continuation only exists after the Google authentication, PIN issuance and
    PIN verification steps, and each of those already spends its own budget
    (`google-auth-ip`, `pin-ip`, `verify-ip`). What this route can be asked to do
    is therefore bounded by those upstream controls rather than by anything here.

    A fail-closed limiter would put a Redis dependency on the last step of account
    creation. While Redis is unavailable a user holding a valid continuation would
    be refused, and the continuation's 15-minute life means a prolonged outage
    costs them the registration outright rather than delaying it. That is a real
    cost, weighed against an incremental saving nothing has yet demonstrated.

    Revisit this if the single-use invariant changes, if the upstream budgets are
    removed or loosened, or if registration volume makes the route measurably
    expensive. Until then the absence of a budget here is intended. Do not close
    the gap by copying `finalize-ip`'s threshold or naming a new namespace: those
    numbers are sized for password work this route does not perform.

---

## Next Step

**Uncommitted work exists right now.** The login timing-test fix is implemented
and fully verified but **not committed**. Four paths are modified or new, all in
`internal/api/auth/login/`:

- `types.go` — adds `PasswordVerifier`
- `service.go` — field and constructor parameter typed as that interface
- `dummy_verification_seam_test.go` — new, the four deterministic tests
- `login_rate_limit_service_test.go` — warm-up and both-orders measurement

The change has been reviewed in full and verified (`go build`, `go vet`,
`go test ./... -count=1`, and `go test -race ./... -count=1` twice, all clean).
The immediate next action is to review `git status` and commit it. Do **not**
re-run the suite first — it has already passed against exactly this tree.

After that, resume the standing order below in a fresh session.

Enrichment and automatic organization are both implemented and verified: the
synchronous per-item endpoint, the background enrichment worker, and the
background organization worker it hands off to. Their architecture is settled —
event-driven, no sweep, no persisted organization state — and is recorded in
decisions 8, 9, 13, 13a and 13b rather than reopened here.

Current order:

1. Deployment work that cannot start until the hosting platform is chosen:
   verify real forwarding-header behaviour, configure trusted proxies from
   verified information, and confirm client-address resolution in that
   environment. See "Outstanding deployment work" above.
2. Email provider selection and production configuration, which the per-email
   budget currently stands in for. See "Outstanding deployment work" above.
3. Decide whether to run a one-off backfill of items enriched before automatic
   organization existed. It is a product decision, not a mechanism gap, and it
   would be a separate deliberate operation rather than anything the worker does
   on its own.
4. Rate limiting for endpoints beyond the PIN endpoints, manual login, the Google
   authentication routes and token refresh, once there is real traffic to size the
   limits against. Every threshold added so far is an engineering estimate, and
   the refresh one in particular is derived from the access-token lifetime rather
   than observed.
5. Cloud Run deployment specifics for the worker, when a target is chosen.

Rate limiting for the PIN endpoints is implemented and verified: per-email and
per-IP budgets, Redis-backed atomic counters, `Retry-After`, and fail-closed
behaviour on Redis failure. Manual login has a per-IP and a per-email budget,
and Google authentication has a per-IP budget, all verified the same way. What
remains for them is deployment configuration — principally the thresholds, which
are estimates rather than measurements — not application code.

Search pagination, autocomplete, search suggestions, and Saved Item update/edit
remain outside the current scope. Collection _deletion_ now exists, but only as the
explicit `DELETE /collections/:id`; collection listing and creation as standalone
operations, a general batch move of saved items, and collection rename remain
outside the current scope.

---

## Resume Instructions

1. Read `AGENTS.md`, `documentation/product.md`, and this file before coding.
2. Check `git status` and `git diff` to understand the actual working tree.
3. **If you intend to run `-race`, verify `where.exe gcc` resolves and
   `go env CGO_ENABLED` is `1` first.** A shell started before the Windows
   `Path` change keeps the old one; see the note under "Testing and verification
   status" for the per-command workaround.
4. Inspect the relevant existing feature packages before changing code.
5. Run:
   `go build ./... && go vet ./... && go test ./...`
   before assuming the tree is healthy.
6. Avoid unrelated refactors. If something looks wrong outside the current task,
   report it instead of fixing it as a side effect.
7. Never edit generated files manually. Change their source and regenerate.
8. Never apply migrations or create commits unless explicitly instructed.
9. Both enrichment paths exist and both work. A save commits its row and queues
   background enrichment, and a completed enrichment that produced a platform
   hands off to background automatic organization. On-demand enrichment of one
   Saved Item still exists as well, and both write the same rows. Read the
   background worker and automatic organization sections above before changing
   either; neither is an open question.
