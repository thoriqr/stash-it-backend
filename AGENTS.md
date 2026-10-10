# AGENTS.md

Operating guide for coding agents. The existing implementation is the source of
truth — when this file and the code disagree, the code wins. Update this file only
if explicitly asked.

## Identity

- Module `github.com/thoriqr/stash-it-backend`, Go `1.27.0` (match `go.mod`; don't bump).
- Fiber **v3**; handlers are `func(c fiber.Ctx) error` — `fiber.Ctx` is a **value** type.
- Postgres via `pgx/v5` + `pgxpool` directly. No ORM, no query builder.
- Swagger 2.0 via `swaggo/swag`, served at `/docs/*`. `zap` logging, `validator/v10` validation.
- Project progress and current implementation status belong in `PROGRESS.md`.

## Architecture

Vertical slices by feature; each owns its application stack. **No DI container** —
wiring is explicit, by hand, in `module.go`.

```text
cmd/api/main.go               bootstrap only; feature modules are registered here
cmd/worker/main.go            bootstrap only; background task handlers are registered here
internal/api/<module>/        routes.go handler.go service.go service_validation.go
                              repository.go mapper.go request.go response.go
                              error_codes.go constants.go types.go generated/ mocks/
internal/api/auth/module.go   composition root for auth sub-features
internal/middleware/auth.go   JWT bearer guard
internal/middleware/ratelimit.go
                              per-client-IP limit; resolves the address via c.IP()
                              and fails closed. Mounted per route.
internal/{apperror,httpx,validation,email,security,config,logger,health,database}/
internal/ratelimit/          Redis-backed fixed-window counters; atomic Lua script
                              returns count and remaining TTL together; HMACs the
                              subject; no Fiber, internal/api, or persistence
internal/enrichment/          reusable metadata extraction; no Fiber, sqlc, Asynq, persistence
internal/worker/queue/        background task contract; no Fiber, internal/api, persistence
internal/worker/<feature>/    worker handler, service, repository, generated/, mocks/
internal/testutil/             testcontainers helpers, fakes, test-only sqlc output
internal/integration/          end-to-end HTTP and worker tests
migrations/                    production schema (source of truth)
docs/                          GENERATED swagger
documentation/                 hand-written project documentation
```

Do not introduce a new architectural pattern when an existing feature already
provides the appropriate pattern.

Do not change architecture, add dependencies, or alter public API contracts
without explicit instruction.

## Conventions

**Handlers are thin** — bind input (`httpx.BindBody`/`BindQuery`), parse path
params (`uuid.Parse(c.Params(...))`), extract device metadata where relevant
(`session.ExtractMetadata`), call the appropriate service method, map the result
(`mapper.go`), and return via `httpx.OK`/`OKWithMeta`/`Created`/`OKMessage`.
Return errors unchanged. No business logic, SQL, or direct DB access in handlers.

**Routes** — `func Routes(router fiber.Router, h *Handler)`. Auth is applied per
protected route with `middleware.Auth(verifier)`, not globally with `router.Use`.

**Services** — exported interface + unexported struct + `NewService` returning
`*service`, plus `...Result` structs where appropriate. Dependencies are narrow
interfaces declared in the consuming package's `types.go`; generate mocks from
those interfaces.

**Repositories** — consumer-defined `Repository` interface; unexported struct
holding `*xxxdb.Queries` plus `*pgxpool.Pool` when it needs a transaction;
`NewRepository` returns the interface. Pass sqlc `Params`/`Row` types through
directly; don't wrap them without a concrete reason.

**Errors** (`internal/apperror`) — construct in the lowest layer that knows the
meaning. Repositories translate driver errors (`pgx.ErrNoRows` →
`NotFoundWith`/`UnauthorizedWith`); services and handlers pass errors through.
Prefer `...With(code, message, err)` when retaining an underlying error.
Do not leak internal error text into API `Message`.

Responses use the shared `httpx` helpers:

```json
{ "data": "...", "message": "...", "meta": "..." }
```

and:

```json
{ "error": { "code": "...", "message": "...", "fields": "..." } }
```

**Transactions** — repositories own their transactions:
`pool.Begin` → `queries.WithTx` → `defer tx.Rollback` → `tx.Commit`.
There is **no shared unit-of-work and no nested transaction across repositories**.
If a change appears to require one, ask rather than inventing a pattern.
Use `SELECT ... FOR UPDATE` for read-modify-write operations that must not race,
and keep rules depending on locked rows inside the transaction.

## Auth and sessions

Security-critical. Leave unchanged unless the task is explicitly about auth.

Implementation lives in `internal/security` and `internal/api/auth/session`.

- Access token = HS256 JWT carrying `sub` (user) and `sid` (session); the verifier
  pins the algorithm and requires both claims.
- Refresh tokens are opaque and stored **hashed only**, rotated through a
  `replaced_by` link. **Reusing an already-replaced token must revoke the entire
  session** — never weaken or bypass this.
- Sessions have both idle and absolute expiry; lifetimes are constants in
  `session/constants.go`.
- Passwords are hashed with **Argon2id**, not bcrypt, in
  `internal/security/password.go`. The parameters, salt and hash are stored
  together as a PHC string, so verification reconstructs the parameters the hash
  was made with and existing hashes stay verifiable after the parameters are
  strengthened. Do not introduce bcrypt. Verification codes are HMAC'd with a
  configured secret. Never store or log plaintext secrets, tokens, passwords, or
  codes.
- **Password work is bounded by a shared capacity limit, not by a rate limit.**
  `security.PasswordWorkLimiter` limits how many derivations run at once across
  the whole process, and it lives **inside the shared `PasswordHasher`** — so
  `Hash`, `Verify` and `DummyVerify` are all bounded and a new call site cannot
  reach Argon2id without passing through the bound. Build one limiter and one
  hasher in `internal/api/auth/module.go` and share them; a limiter per feature
  would make the total the sum of the parts. `DummyVerify` is bounded too and has
  to be: it costs the same derivation, so an exemption would be the cheapest way
  to exceed the limit. This is **not** `ratelimit.Limiter`: a rate limit bounds
  how often a subject submits, which composes across subjects and bounds nothing
  about what is running now. `Acquire` hands back a per-acquisition release
  closure rather than the limiter exposing an unowned `Release()`, because a
  release that ran twice would otherwise free a slot a live operation was
  relying on. Exhaustion is `503` with a bounded `Retry-After`; it is not
  invalid credentials, because the password was never checked, and it is not a
  429, because no budget was spent.
- Email is normalized once, by `registration.NormalizeEmail` (trim, then
  lowercase). Registration stores the normalized form, so every read path has to
  compare against it; PostgreSQL's `=` on `TEXT` is case sensitive.
- Email flows are non-enumerating by design.
- Keep Google ID-token verification behind `login.GoogleTokenVerifier` so tests
  can fake it.
- Registration / password-reset verification endpoints are intentionally
  unauthenticated; the existing `verification_id` + emailed PIN flow is the
  credential.
- **Rate limiting is per flow, and a flow's budgets must not be reused by
  another.** Manual login has `login-ip` (route middleware) and `login-email`
  (service); all three Google authentication routes share `google-auth-ip` (route
  middleware). Google login and manual login are two doors into the same
  application: neither flow's traffic may lock the other out, and because the
  limiter fails closed, one namespace counting both would let a single exhausted
  counter take both doors down. Do not merge the namespaces.
- **The Google budget is a ceiling on cost, not on credential guessing.** The
  Google routes have no secret to try, so nothing there is about brute force; what
  it bounds is token verification and the session, confirmation and pending-row
  writes a caller holding one valid token could otherwise repeat indefinitely. Do
  not add a per-email budget to these routes: the email would come from a
  Google-verified claim the caller does not choose, so it could only ever be spent
  by the legitimate owner, making it a lockout rather than a protection.
- **Limiter thresholds in `internal/api/auth/login/constants.go` are initial
  engineering estimates, not measured production values.** No deployment has been
  selected and no traffic observed. Do not treat them as validated, and do not
  lower one to match another without saying which shared-address false positive
  that trades away.

## Outbound fetches and enrichment

Security-critical. `internal/security` is the outbound-fetch boundary and
`internal/enrichment` is reusable extraction logic. The synchronous Saved Item
integration lives under `internal/api/enrichment`; the background Asynq one lives
under `internal/worker/enrichment` and consumes the same `internal/enrichment`
capability, so the two paths cannot diverge on what a page error means.

- `security.NewGuardedHTTPClient(policy)` is the only sanctioned way to fetch a
  user-supplied URL. Its `net.Dialer.ControlContext` validates the **resolved**
  address at dial time. Do not add a second outbound client, transport, proxy
  path, or address allow/deny list elsewhere.
- Allowed schemes are `http`/`https` and allowed ports are 80/443 only. The
  blocked address ranges live in `internal/security`; don't duplicate or extend
  them elsewhere.
- `Proxy` is nil on the guarded transport deliberately.
- `internal/enrichment` receives its `*http.Client` by constructor injection and
  must never build one, use `http.DefaultClient`, or call `http.Get`. Keep it
  independent of Fiber, `internal/api`, sqlc, Asynq, and persistence.
- Enrichment operates on one Saved Item at a time. Ownership is scoped by
  `id AND user_id`; missing and foreign items must have the same not-found
  behavior. Background tasks are the documented exception: a task names the row it
  acts on, so the worker packages read by `id` alone and verify the owner against
  the payload rather than by scoping the read.
- Enrichment must not touch `collection_id` or move an item between collections.
  Automatic organization is a separate concern.
- `platform` is derived from metadata the page publishes about itself, never
  from the URL hostname. Do not add host-based inference or domain→platform
  mapping.
- A successful enrichment means the process ran, not that every field was found.
  A page exposing no metadata is a successful enrichment with an empty result.
- A failed enrichment is represented by `enrichment_status`, not an HTTP 5xx.
  A failed attempt must preserve existing metadata and must not update
  `last_enriched_at`.
- `enrichment_status` has exactly three values: `pending`, `completed`,
  `failed`. Do not add a `processing` or `retrying` value to represent queue
  execution mechanics.
- Enrichment is repeatable and must not be gated on the current
  `enrichment_status`.
- `Author` and `SiteName` are extraction-only unless persistence is explicitly
  changed.
- Metadata URLs may be resolved to absolute form but are never fetched merely
  because they appear in metadata.

## Background enrichment worker

`cmd/worker` is a standalone binary with no HTTP surface. It imports nothing from
`internal/api`, and its persistence and application code lives under
`internal/worker`.

- `internal/worker/queue` is the whole contract between the two binaries: the
  task type, the queue name, the payload type, the task timeout and retry budget,
  and the producer the API enqueues through. It must stay free of Fiber,
  `internal/api`, and persistence, so the two sides cannot drift.
- `internal/api/saved_item` depends on its own one-method enqueuer interface, not
  on the queue package, and the enqueuer may be nil.
- A save commits its row **before** queueing the item's id. Enqueueing earlier
  could name an id the database never accepted; a failed enqueue leaves a valid
  Saved Item that is still `pending`. There is no reconciliation pass. Treat both
  as accepted trade-offs rather than defects.
- The task payload carries the saved item id only. The worker reads the URL from
  the row, so a queued copy can never disagree with what was stored.
- The worker reuses `internal/enrichment` and `internal/security`. Do not give it
  a second extraction layer or outbound client.
- Retry classification is read from `enrichment.Kind`; the worker maps that
  classification onto a queue decision and must not restate what a page error
  means. Retries are bounded by the queue's own budget and capped backoff.
- Never log a full Redis URL. It may carry credentials in its userinfo section;
  log the parsed address and database instead.

## Automatic organization

`internal/worker/organization` files a Saved Item into a collection named by its
enriched platform. It is a separate task type on its own queue, scheduled by the
enrichment worker, not a phase of enrichment.

- **Event-driven only.** A task is produced by a successful enrichment that
  produced a platform, and by nothing else. Do not add a scan, a sweep, a periodic
  query, or a "not yet organized" lookup: there is no persisted organization state,
  so anything that needed one would be inventing a second source of truth.
- **The decision is made at execution time, against a locked row.** The payload
  says which item to look at, never where it goes. `enrichment_status`,
  `platform` and the current collection are all re-read.
- **Only an item still in `Unsorted` may be moved.** Being in `Unsorted` when
  enrichment finished grants nothing. This is what keeps organization from
  overriding a user's own filing, and it must not be relaxed to "organize unless
  the target differs".
- **Reuse a matching collection; never rename one.** Whether the user named it or
  an earlier item created it, a match is a target. `system` describes who created
  a collection, not who owns it: it still belongs to one user.
- **`system_key` is the platform's own normalized identity**, derived from the
  platform value, not a curated mapping. Deriving one from a hostname or a domain
  table would contradict decision 3 above.
- **Organization never writes enrichment state.** No `organization_status` column,
  and `MoveSavedItemToCollectionForOrganization` touches `collection_id` alone. A
  failed organization leaves the item `completed`.
- **No-op conditions are successes.** A missing item, an incomplete enrichment, a
  NULL platform and an item the user already filed are all finished tasks. An
  ownership mismatch is the one condition that is reported and archived, because it
  cannot happen through any real path and would otherwise hide a bug.

**A saved item's create response and its detail response are deliberately different
shapes.** Do not unify them into one DTO.

- `POST /saved-items` reports **`id`, `url`, `collection_id` and `enrichment_status`
  only**. A save commits before enrichment has run, so `domain` is the only metadata
  column carrying a value and it was derived locally from the submitted URL rather
  than read off the page. Reporting the rest would be unlooked-up nulls presented as
  metadata, which reads as "this page has nothing" rather than "this page has not been
  read yet".
- `enrichment_status` is included because the `INSERT` returns it — it costs no
  additional query, and reporting the stored value beats reporting a constant this
  code assumes.
- `GET /saved-items/:id` reports the **complete saved item**, the same twelve fields
  `GET /collections/:id/saved-items` reports, plus a `collection` object carrying
  `id` and `name`. Without `enrichment_status` a null title is ambiguous between "no
  title" and "not read yet". `type` and `system_key` stay out: they belong to
  `GET /collections`. Unsorted arrives through the same object as anything else.
- **The collection costs no extra query.** `GetSavedItemByIDForUser` joins
  `collections` in the same statement, owner-scoped on both sides. The join is `INNER`
  because `collection_id` is `NOT NULL` and references an existing row.
- `saved_item.collection_id` always equals `collection.id`; the redundancy makes the
  item self-describing once it leaves the response.

## Ownership and collections

- `collections.user_id` owns a collection and `saved_items.user_id` owns a Saved
  Item. Migration 000025 enforces that a Saved Item may only reference a collection
  its own owner owns, through a composite foreign key over `(collection_id,
  user_id)`. Do not weaken or work around it, and do not write code that assumes
  `collection_id` alone establishes ownership.
- Scope reads and writes by `id AND user_id` wherever there is a user in scope. A
  `NOT NULL` foreign key on `collection_id` is not an ownership check.
- Enrichment must never write `collection_id`. Automatic organization under
  `internal/worker/organization` is the only writer that does, and it only moves an
  item that is still in `Unsorted`.
- **Deleting a saved item must never delete or modify its collection.**
  `DELETE /saved-items/:id` reports the item's collection, whether that collection
  is now empty, and whether the user may delete it. Those are advisory values read
  after the delete, not an action. Removing a collection is a separate endpoint that
  the caller must invoke deliberately.
- `collection_deletable` is the conjunction of the collection being empty and not
  being Unsorted. It is a convenience for the caller, not an authorization decision:
  the delete endpoint re-reads the database and validates again.
- The count backing `collection_empty` is read **after** the delete, never before,
  and it is scoped by `collection_id AND user_id`. There is deliberately no
  transaction around the delete and its two reads: under READ COMMITTED each
  statement takes its own snapshot either way, and there is no state to keep
  consistent because nothing is being acted on.

**Deleting a collection** is `DELETE /collections/:id`, mounted on its own
`/collections` group rather than under `/saved-items`, so it cannot collide with
`DELETE /saved-items/:id`.

- It is owner-scoped by `id AND user_id`, and unknown and foreign collections
  produce the same not-found error.
- **Unsorted cannot be deleted, and `system_key` is the only test for it.** Never
  use `type` or the display name. `type = 'system'` describes who created a
  collection, not what it is, so a system collection is deletable like any other.
- The request body is a stable two-field shape: `saved_items_action` is `delete` or
  `move`, and `target_collection_id` must be null for `delete` and a collection id
  for `move`. Both fields always exist and never change meaning. Nothing is
  defaulted — the two actions have opposite consequences and one destroys content,
  so an absent or contradictory action or target is a 400, never a guess.
- Unsorted is an ordinary move target named by its id. There is no
  `move_to_unsorted` flag, no fallback to Unsorted when a target is missing, and no
  collection-name addressing.
- **The repository owns the whole operation as one transaction:** load the source,
  refuse Unsorted, resolve the target when moving, delete or move the children,
  then delete the collection. The children must be disposed of first because
  `saved_items.collection_id` is `NOT NULL`, so the collection cannot go first.
- Nothing is taken `FOR UPDATE`. The move endpoint locks a saved item and then
  touches a collection, so locking the collection first here would invert that order
  and risk deadlock. Emptiness is decided by the foreign key at the final DELETE
  rather than by a read, which is both race-free and cheaper.
- `saved_items_collection_id_fkey` remains the final guard. When a saved item is
  filed into the collection mid-operation the constraint refuses, the transaction
  rolls back, and the caller is told `COLLECTION_NOT_EMPTY`. Map that violation by
  `ConstraintName`; do not replace it with an application pre-check, and never
  change it to `ON DELETE CASCADE`.
- `MoveSavedItemsToCollection` exists only as the moving half of this operation.
  It is not a general batch move and no endpoint exposes one on its own.

**Listing collections** is `GET /collections`, registered on the same
`/collections` group as the delete. It is currently the only cursor-paginated
endpoint.

- **Unsorted is pinned first under every sort** by a rank over `system_key` in the
  `ORDER BY`, not filtered out and not special-cased per sort, so every ordering
  inherits the rule once. It must never reappear on a later page.
- **Exclude the pinned row with `system_key IS DISTINCT FROM 'unsorted'`.** That is
  NULL-safe as well as selective: `collections_system_key_check` gives every
  `type = 'user'` collection a NULL key, so a plain `<>` evaluates to NULL for all of
  them and a resumed page would return system collections only.
- **Every ordering needs a unique tie-breaker.** `created_at` defaults to `NOW()`,
  which is constant within a transaction, and migration 000022 inserts every user's
  Unsorted collection in one statement, so equal timestamps are a real state.
  `collections.id` is `uuidv7()` with no overriding trigger, so it is unique and
  monotonic in creation order.
- **`name` orders by `lower(btrim(name))`** — the expression `collections_user_name_unique`
  is built on, so the ordering and the uniqueness constraint cannot disagree. A
  cursor's name value must be that same normalized form, never the raw display name.
- **A cursor is a position, not a row reference.** No resumed query may read the row a
  cursor came from, so the position survives that row being deleted or renamed, and a
  cursor pointing past the end returns an empty page rather than an error.
- **`internal/pagination` owns the envelope only** — JSON over
  `base64.RawURLEncoding`, plus a payload version. Cursor payload structs stay with
  their feature: what a page must record differs per listing, and one shared struct
  with optional fields would be a second source of truth.
- **Payload fields that can be absent must be pointers.** A plain `int` group decodes
  an omitted field to 0, which is itself a real group value, so a truncated cursor
  would read as valid and silently skip the pinned collection.
- **`httpx.CursorPage.NextCursor` carries no `omitempty`.** A nil pointer tagged
  `omitempty` is dropped from the JSON, and a client must be able to read
  `next_cursor` and find `null` rather than find the key missing. `Meta.Cursor` is a
  pointer with `omitempty`, so endpoints that do not paginate by cursor leave the key
  out entirely and their responses are unchanged.
- **Fetch `limit + 1` and build the cursor from the last returned row**, never the
  lookahead row: the lookahead row was never sent, so a cursor built from it resumes
  past a collection the client never received.
- Keyset predicates need **explicit casts** on row-comparison parameters. Without them
  sqlc types every parameter from the row's leftmost element, which would give a
  `timestamptz` type to an id parameter.

**Listing a collection's saved items** is `GET /collections/:id/saved-items`, also on
the `/collections` group. There is no standalone saved-item listing: every saved item
belongs to exactly one collection, so an inbox is the Unsorted collection seen through
the same endpoint.

- It reports the **full** saved item, including `description`, `image_url`,
  `collection_id`, `enrichment_status` and `last_enriched_at`. The field is named
  `last_enriched_at`, matching the column and the enrichment feature's DTO. A listing
  that omitted the enrichment columns could not tell a caller whether an item's
  metadata had arrived, which is the difference between "this page has no title" and
  "this page has not been read yet".
- **`data.collection` carries `id` and `name` and nothing else.** The caller already
  arrived knowing the id; what it cannot know from the path is the name. Reuse the row
  `GetCollectionByIDForUser` already returned rather than reading again — a second
  lookup would be a round trip to fetch a row already in hand. Report it on every
  response, including an empty page, because an empty collection is still a named
  collection and omitting the field would make "nothing in it" look like "no match".
  Unsorted goes through the same type as everything else.
- **A null title and a failed enrichment are ordinary states, not absences.** Both are
  listed. `enrichment_status` is `NOT NULL` so it is never null; the metadata columns
  are, so they are pointers in the response.
- **It is ordered `created_at DESC, id DESC` with no sort option.** A collection is
  something a person curated, and offering orders for it would answer a question this
  resource does not pose. `GET /collections` is where ordering is a choice.
- **Listing must never trigger enrichment or write anything.** Enrichment is scheduled
  by saving and by an explicit per-item request; browsing is not a reason to fetch.
- **Ownership is proved before any item is read**, by resolving the collection with
  `id AND user_id`. Without that the listing would be scoped by collection id alone.
- Its cursor payload is **`ListSavedItemsInCollectionCursor`**, which has no `Sort` and
  no `Group`. Both exist on the collection payload only because that listing pins
  Unsorted and offers three orders; carrying fields that can never vary here would mean
  writing validation branches that cannot be reached.
- Only **two** queries are needed, not six: one ordering and no pinned row means one
  first-page statement and one after-position statement.
- **The service validates and parses a cursor's position before any query runs.** A
  cursor position arrives as a string, so the time-based sorts must check it really is
  a timestamp and record the parsed `pgtype.Timestamptz` on the cursor. The repository
  then passes the value on without reinterpreting it, so a caller-supplied token can
  never turn into a server fault: an unusable one is `INVALID_CURSOR`, and only a real
  driver error is `INTERNAL_SERVER_ERROR`. A derived field must be `json:"-"` so it
  cannot become a second copy of the position that disagrees with the wire value.
- Collection names are compared and stored the way `lower(btrim(name))` and a
  trimmed display name already define. Reuse that semantics rather than writing a
  second normalization: a collection whose stored name disagrees with the name the
  unique index matched cannot be found again.

## Data integrity

Database constraints encode business rules. **Do not drop or weaken them to make
a test pass** — fix the code. When adding an allowed value or schema rule, use
a migration.

## Migrations and baseline schema

Two schemas have different roles and **can drift**.

- `migrations/` — production database schema and **source of truth**, as
  `NNNNNN_name.up.sql` / `.down.sql` pairs. **Never edit an existing migration**
  once created — add a new one instead.
- Create a migration with:

```sh
migrate create -ext sql -dir migrations -seq <migration_name>
```

- **Creating and applying a migration are separate operations.**
- **Never apply a migration to any database unless the user explicitly instructs
  it.** Do not run `migrate ... up` or an equivalent command.
- Do not invent database URLs, environments, or migration targets.
- If a schema change is required, create the migration and report the SQL/diff
  for review unless the user explicitly asked for application.
- `internal/database/baseline/schema.sql` is a manually maintained snapshot,
  embedded with `//go:embed`, used only by test-only sqlc targets and throwaway
  Postgres containers. It is not used at runtime.
- Creating a migration does **not** update the baseline. Update the baseline only
  when asked, and preserve its formatting and organization.

## Code generation

Generated code is **output**. Never hand-edit it. Change the source, then
regenerate.

Output paths include:

```text
internal/**/mocks/
internal/api/**/generated/
internal/testutil/db/**/generated/
docs/
```

- **mockgen** — generated from the interfaces consumed by a package. Change the
  source interface first, then regenerate the affected mock.
- **sqlc** — `queries.sql` files are the source. `sqlc.yaml` defines targets,
  packages, output directories, and pgx configuration. After query changes, run
  `sqlc generate`.
- Integration-test queries live under `internal/testutil/db/<feature>/queries.sql`
  and generate into the sibling `generated/` directory.
- Every current sqlc target uses `omit_unused_structs: true`, so generated
  persistence models are target-specific. Do not introduce shared domain types
  merely to avoid similar structs. A feature may own a projected type in
  `types.go` while keeping sqlc `Params`/`Row` types generated.
- A generated model may be shared when another feature genuinely consumes it
  and the owning sqlc target continues to generate it.
- **swag** — edit handler annotation comments, then regenerate with:

```sh
swag init -g cmd/api/main.go -parseInternal
```

Features live under `internal/api/`. Follow the existing `sqlc.yaml` mapping for
the relevant feature.

`collection` registers two groups. Its saved-item-scoped endpoint mounts under the
same `/saved-items` prefix as `saved_item` (`PUT /saved-items/:id/collection`), and
its own resource mounts at `/collections` (`DELETE /collections/:id`). Fiber groups
are additive, so neither registration touches the routes the other already
registers. The split is deliberate: `DELETE /saved-items/:id` deletes a saved item
and `DELETE /collections/:id` deletes a collection, and sharing one group would let
one shadow the other.

## API documentation

Every handler carries a `// Name godoc` swag block with summary, description,
params, success/failure responses, and route. Global Swagger annotations and
`BearerAuth` live on `main()` in `cmd/api/main.go`.

`internal/api/swagger/` contains documentation-only response shapes; never import
it from runtime code.

**A feature's `...APIResponse` must declare its `meta`, and that field is
documentation-only.** `httpx.OKWithMeta` builds the real envelope, so an API response
struct that omits `meta` still returns a three-key body while the generated schema
describes two — and any `@Description` mentioning `meta.cursor` then contradicts the
schema beneath it, which is still valid OpenAPI and so passes every linter. Declare a
swag-only mirror (`ListCollectionsMeta`, `ListSavedItemsInCollectionMeta`) sharing one
`CursorPageMeta`, and never populate the field. `session.ListSessionsMeta` is the
pre-existing precedent. Because the drift is invisible at runtime, assert it in a test:
`internal/integration/collection_saved_items_api_test.go` walks `docs/swagger.json`
and the real body side by side and fails if either gains or loses a key.

Generated Swagger belongs in `docs/`. Hand-written project documentation belongs
in `documentation/`.

## Testing

- Unit tests are colocated with source as `_test.go`, following existing naming
  conventions such as `*_service_test.go` and `*_helpers_test.go`.
- Use `testify` and `go.uber.org/mock`. Mock consumer interfaces from `types.go`
  and regenerate mocks rather than editing them.
- Integration tests live in `internal/integration/`. The shared `TestMain`
  starts one Postgres testcontainer, applies the baseline schema, builds the real
  app via `testutil.NewApp`, then tears it down. Worker tests additionally start a
  Redis testcontainer, lazily and only when one is needed. **Docker is required.**
- Because the integration database and the Redis queue namespace are shared, tests
  must not collide on either.
- Prefer asserting error codes over error strings and testing business behavior
  at the service/repository level rather than testing handler plumbing.
- Run `go test -race` on any package whose change adds or alters concurrent code.
  It needs cgo and a C compiler; where none is installed, say so plainly rather
  than implying the detector ran. **Repeated runs are not a substitute**: `-count`
  exercises deadlock and liveness, not data races, and a concurrency change
  verified only that way has not been verified.

## Verify changes

For normal backend changes:

```sh
go build ./...
go vet ./...
go test ./...
```

If generated sources changed, run the relevant generators as well.

Report which verification commands were actually run and whether they passed or
failed. **Never claim tests pass unless they were actually run.** If Docker is
unavailable, state that plainly when integration tests cannot run.

## Rules

- Inspect the code rather than guessing; prefer existing patterns over new ones.
- Keep changes scoped to the requested task.
- Do not perform unrelated refactors.
- Do not add dependencies without explicit instruction.
- Do not change architecture, public API contracts, or auth/session behavior
  without explicit instruction.
- Never manually edit generated files.
- Never apply database migrations without explicit instruction.
- If a requested change conflicts with an existing architectural or security
  invariant, stop and explain the conflict before implementing a workaround.
