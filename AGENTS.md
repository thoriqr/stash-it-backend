# AGENTS.md

Operating guide for coding agents. The existing implementation is the source of
truth — when this file and the code disagree, the code wins. Inspect the relevant
code and tests rather than guessing. Update this file only if explicitly asked.

This file says what to do and not do. The documents under `documentation/` explain
why; see "Reference documentation" at the end.

## Stack

- Module `github.com/thoriqr/stash-it-backend`, Go `1.27.0` (match `go.mod`; do
  not bump).
- Fiber **v3**. Handlers are `func(c fiber.Ctx) error`; `fiber.Ctx` is a **value**
  type.
- PostgreSQL through `pgx/v5` and `pgxpool` directly. No ORM, no query builder.
- Swagger 2.0 through `swaggo/swag`, served at `/docs/*`. Logging is `zap`;
  validation is `validator/v10`.

## Commands

```sh
go build ./...
go vet ./...
go test ./...                                # integration tests need Docker
go test -race ./path/to/pkg                  # needs cgo and a C compiler
sqlc generate                                # after editing a queries.sql
swag init -g cmd/api/main.go -parseInternal  # after editing handler annotations
migrate create -ext sql -dir migrations -seq <migration_name>
```

## Architecture

Vertical slices by feature; each owns its application stack. **No DI container** —
wiring is explicit, by hand, in `module.go`.

```text
cmd/api/main.go                bootstrap only; feature modules are registered here
cmd/worker/main.go             bootstrap only; standalone binary, no HTTP surface,
                               imports nothing from internal/api
internal/api/<module>/         routes.go handler.go service.go service_validation.go
                               repository.go mapper.go request.go response.go
                               error_codes.go constants.go types.go generated/ mocks/
internal/api/auth/module.go    composition root for auth sub-features
internal/middleware/           auth.go: JWT bearer guard
                               ratelimit.go: per-client-IP limit; resolves the
                               address via c.IP() and fails closed; mounted per route
internal/ratelimit/            Redis-backed limiters (fixed-window and rolling-window),
                               atomic Lua scripts, HMACs the subject.
                               No Fiber, internal/api, or persistence
internal/pagination/           cursor envelope only
internal/security/             outbound-fetch boundary, password hashing, session and
                               token primitives
internal/enrichment/           reusable metadata extraction.
                               No Fiber, sqlc, Asynq, or persistence
internal/worker/queue/         API/worker task contract.
                               No Fiber, internal/api, or persistence
internal/worker/<feature>/     worker handler, service, repository, generated/, mocks/
internal/{apperror,httpx,validation,email,config,logger,health,database}/
internal/testutil/             testcontainers helpers, fakes, test-only sqlc output
internal/integration/          end-to-end HTTP and worker tests
migrations/                    production schema (source of truth)
docs/                          GENERATED swagger
documentation/                 hand-written project documentation
```

Features do not import one another. Do not introduce a new architectural pattern
when an existing feature already provides the appropriate one. Do not change
architecture, add dependencies, or alter public API contracts without explicit
instruction.

## Conventions

**Handlers are thin.** Bind input (`httpx.BindBody` / `BindQuery`), parse path
params (`uuid.Parse(c.Params(...))`), extract device metadata where relevant
(`session.ExtractMetadata`), call the service, map the result (`mapper.go`), and
return via `httpx.OK` / `OKWithMeta` / `Created` / `OKMessage`. Return service
errors unchanged. No business logic, SQL, or direct DB access in handlers.

**Routes.** `func Routes(router fiber.Router, h *Handler)`. Apply auth per protected
route with `middleware.Auth(verifier)`, never globally with `router.Use`.

**Services.** Exported interface, unexported struct, `NewService` returning
`*service`, plus `...Result` structs where appropriate. Dependencies are narrow
interfaces declared in the consuming package's `types.go`; generate mocks from them.

**Repositories.** Consumer-defined `Repository` interface; unexported struct holding
`*xxxdb.Queries` plus `*pgxpool.Pool` when it needs a transaction; `NewRepository`
returns the interface. Pass sqlc `Params` / `Row` types through directly; do not
wrap them without a concrete reason. Keep SQL and direct DB access in repositories.

**Errors** (`internal/apperror`). Construct in the lowest layer that knows the
meaning. Repositories translate driver errors (`pgx.ErrNoRows` →
`NotFoundWith` / `UnauthorizedWith`); services and handlers pass errors through.
Prefer `...With(code, message, err)` when retaining an underlying error. Never
leak internal error text into API `Message`.

**Responses** use the shared `httpx` helpers, never another envelope:

```json
{ "data": "...", "message": "...", "meta": "..." }
```

```json
{ "error": { "code": "...", "message": "...", "fields": "..." } }
```

**Transactions.** Repositories own them:
`pool.Begin` → `queries.WithTx` → `defer tx.Rollback` → `tx.Commit`. There is **no
shared unit-of-work and no nested transaction across repositories**; if a change
appears to need one, ask. Use `SELECT ... FOR UPDATE` for read-modify-write
operations that must not race, and keep rules that depend on locked rows inside
the transaction. Do not change locking behaviour as incidental cleanup.

## Invariants

Do not change these without explicit instruction. Leave authentication and session
behaviour unchanged unless the task is explicitly about auth. Each rule says what it
protects, so a "simplification" that breaks it is recognisable.

**Authentication and secrets**

- Never delete individual refresh-token rows. Delete the parent session and let the
  cascade remove the chain: deleting a successor erases the signal that its
  predecessor was already replaced, which silently disables reuse detection. Reusing
  an already-replaced token revokes the whole session (`auth-sessions.md`).
- Passwords use Argon2id, never bcrypt. All password work goes through the shared
  `PasswordHasher`, which holds the `PasswordWorkLimiter`; a call site that bypasses
  it stops the limit from bounding anything.
- Normalize email once, with `registration.NormalizeEmail`, and compare the
  normalized form. PostgreSQL text equality is case sensitive.
- Account linking matches both `provider` and `provider_subject`, never an email, and
  is gated on mailbox control rather than on the existing account's password. A
  `confirmation_id` is a flow identifier, not a credential. Keep Google verification
  behind `login.GoogleTokenVerifier` so tests can fake it.
- Never store or log plaintext secrets, tokens, passwords or codes, and never log a
  full Redis URL.

**Rate limiting**

- A limiter that cannot answer refuses the request; it never admits it.
- Each flow has its own budget namespace. Do not merge them: one exhausted counter
  would lock out an unrelated flow. Do not add a per-email budget to the Google
  routes, where the email comes from a verified claim the caller does not choose.
  Thresholds are unmeasured estimates. Read `documentation/pin-email-budgets.md`
  before changing PIN email budgets, the cooldown, or the email sender contract.

**Outbound requests and enrichment**

- Fetch user-supplied URLs only through `security.NewGuardedHTTPClient`. Do not add a
  second client, transport or proxy path. `internal/enrichment` receives its client by
  injection and never builds one, so no fetch can bypass the guard.
- `platform` comes from the metadata a page publishes, never from the URL hostname.
  `enrichment_status` has exactly three values: `pending`, `completed`, `failed`.
  Enrichment never writes `collection_id`.

**Worker and organization**

- A save commits its row before its id is queued. A failed enqueue leaves a valid
  `pending` item and is accepted, not repaired. The task payload carries the item id
  only, and worker packages read by `id` alone and verify the owner against the
  payload.
- Automatic organization is event-driven: it runs only after a successful enrichment,
  moves only an item still in `Unsorted`, never scans or sweeps, and keeps no
  organization status of its own.

**Collections and listings**

- Scope reads and writes by `id AND user_id`. A foreign key alone is not an ownership
  check.
- `Unsorted` is identified by `system_key` only and cannot be deleted.
  `saved_items_collection_id_fkey` is the final guard against deleting a non-empty
  collection; never change it to `ON DELETE CASCADE`.
- A cursor is a position, not a row reference. Payload fields that can be absent are
  pointers, `NextCursor` has no `omitempty`, a page fetches `limit + 1` rows and builds
  its cursor from the last returned row, and every ordering has a unique tie-breaker.
- The Inbox (`GET /saved-items`) is not `Unsorted`. A saved item's create and detail
  responses are deliberately different shapes; do not unify them.

Read `documentation/decisions.md` before changing authentication, account linking,
saved items, collections, enrichment, the worker, or rate limiting. It records why
they are built this way.

## Migrations and data integrity

Database constraints encode business rules. **Do not drop or weaken them to make a
test pass** — fix the code.

- `migrations/` is the production schema and **source of truth**, as
  `NNNNNN_name.up.sql` / `.down.sql` pairs. **Never edit an existing migration**;
  add a new one. When adding an allowed value or schema rule, use a migration.
- **Creating and applying a migration are separate operations.** Never apply a
  migration to any database unless the user explicitly instructs it. Do not run
  `migrate ... up` or an equivalent. Do not invent database URLs, environments, or
  migration targets.
- If a schema change is required, create the migration and report the SQL/diff for
  review unless the user explicitly asked for it to be applied.
- `internal/database/baseline/schema.sql` is a manually maintained snapshot, embedded
  with `//go:embed`, used only by test-only sqlc targets and throwaway Postgres
  containers. It is not used at runtime and **can drift** from the migrations.
  Creating a migration does **not** update it. Update the baseline only when asked,
  and preserve its formatting and organization.

## Code generation

Generated code is **output**. Never hand-edit it; change the source, then regenerate.

```text
internal/**/mocks/
internal/api/**/generated/
internal/testutil/db/**/generated/
docs/
```

- **mockgen:** generated from the interfaces a package consumes. Change the source
  interface first, then regenerate the affected mock.
- **sqlc:** `queries.sql` files are the source; `sqlc.yaml` defines targets, packages,
  output directories and pgx configuration. Run `sqlc generate` after query changes
  and follow the existing mapping for the feature. Integration-test queries live under
  `internal/testutil/db/<feature>/queries.sql` and generate into the sibling
  `generated/` directory.
- Every sqlc target uses `omit_unused_structs: true`, so generated models are
  target-specific. Do not introduce shared domain types merely to avoid similar
  structs. A feature may own a projected type in `types.go` while keeping sqlc
  `Params` / `Row` generated. A generated model may be shared when another feature
  genuinely consumes it and the owning target keeps generating it.

## API documentation

- Every handler carries a `// Name godoc` swag block with summary, description,
  params, success and failure responses, and route. Global annotations and
  `BearerAuth` live on `main()` in `cmd/api/main.go`. Edit the annotations, then
  regenerate.
- `internal/api/swagger/` holds documentation-only response shapes; never import it
  from runtime code.
- **A feature's `...APIResponse` must declare its `meta`, and that field is
  documentation-only.** `httpx.OKWithMeta` builds the real envelope, so a response
  struct that omits `meta` still returns three keys while the schema describes two,
  and still passes every OpenAPI linter. Declare a swag-only mirror sharing one
  `CursorPageMeta` (`session.ListSessionsMeta` is the precedent) and never populate
  it. Keep the test that walks `docs/swagger.json` against a real body passing
  (`internal/integration/collection_saved_items_api_test.go`).
- Generated Swagger belongs in `docs/`; hand-written documentation in `documentation/`.
  Swagger is the reference for API contracts.

## Testing

- Unit tests are colocated with source as `_test.go`, following existing naming such
  as `*_service_test.go` and `*_helpers_test.go`.
- Use `testify` and `go.uber.org/mock`. Mock consumer interfaces from `types.go` and
  regenerate rather than editing mocks.
- Integration tests live in `internal/integration/`. The shared `TestMain` starts one
  Postgres testcontainer, applies the baseline schema, builds the real app via
  `testutil.NewApp`, then tears it down. Worker tests also start a Redis testcontainer,
  lazily. **Docker is required.**
- The integration database and Redis queue namespace are shared; tests must not
  collide on either.
- Prefer asserting error codes over error strings, and test business behaviour at the
  service/repository level rather than handler plumbing.
- Run `go test -race` on any package whose change adds or alters concurrent code. It
  needs cgo and a C compiler; where none is installed, say so rather than implying the
  detector ran. **Repeated runs are not a substitute**: `-count` exercises deadlock and
  liveness, not data races.

## Verify and finish

For normal backend changes run `go build ./...`, `go vet ./...` and `go test ./...`;
run the relevant generators if generated sources changed.

Report only commands actually run and whether they passed. **Never claim a check
passed that was not run.** If Docker or another requirement prevents a check, say so
plainly. Before finishing:

- Review the diff for unrelated changes and accidental generated-file edits.
- Confirm that architecture, API contracts and auth/session behaviour changed only
  when explicitly requested.
- Summarize the changes and important trade-offs, and list the tests, generators and
  verification commands actually run, including checks that could not run and why.

## Rules

- Inspect the code rather than guessing; prefer existing patterns over new ones.
- Keep changes scoped to the requested task; no unrelated refactors.
- Do not add dependencies without explicit instruction.
- Do not change architecture, public API contracts, or auth/session behaviour without
  explicit instruction.
- Never manually edit generated files. Never apply database migrations without
  explicit instruction.
- If a requested change conflicts with an existing architectural or security
  invariant, stop and explain the conflict before implementing a workaround.
- Write comments only for context the code cannot convey: a design decision, a
  constraint, non-obvious behaviour. Explain why, not what.

## Reference documentation

Read the relevant document before changing the area it covers.

| Area                                                                               | Read                                 |
| ---------------------------------------------------------------------------------- | ------------------------------------ |
| Auth, account linking, saved items, collections, enrichment, worker, rate limiting | `documentation/decisions.md`         |
| Sessions, token rotation, reuse detection, expiry                                  | `documentation/auth-sessions.md`     |
| Dead-session cleanup                                                               | `documentation/session-cleanup.md`   |
| PIN email budgets, cooldown, sender contract                                       | `documentation/pin-email-budgets.md` |
| Product scope, principles and roadmap                                              | `documentation/product.md`           |
| How the product behaves, feature by feature                                        | `documentation/product-behavior.md`  |
