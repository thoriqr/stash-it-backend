# AGENTS.md

Operating guide for coding agents. The existing implementation is the source of
truth — when this file and the code disagree, the code wins. Update this file only
if explicitly asked.

## Identity

- Module `github.com/thoriqr/stash-it-backend`, Go `1.27.0` (match `go.mod`; don't bump).
- Fiber **v3**; handlers are `func(c fiber.Ctx) error` — `fiber.Ctx` is a **value** type.
- Postgres via `pgx/v5` + `pgxpool` directly. No ORM, no query builder.
- Swagger 2.0 via `swaggo/swag`, served at `/docs/*`. `zap` logging, `validator/v10` validation.
- Scope today: authentication (`internal/api/auth`), saved items (`internal/api/saved_item`),
  and collections (`internal/api/collection`). `internal/api/user` has a repository and
  generated code but no service/handler/routes — scaffolding, not dead code.

## Architecture

Vertical slices by feature; each owns its whole stack. **No DI container** —
wiring is explicit, by hand, in `module.go`.

```text
cmd/api/main.go               bootstrap only; calls auth.RegisterModule, no feature details
internal/api/<module>/        routes.go handler.go service.go service_validation.go
                              repository.go mapper.go request.go response.go
                              error_codes.go constants.go types.go generated/ mocks/
internal/api/auth/module.go   composition root for auth sub-features (wire there only)
internal/middleware/auth.go   JWT bearer guard
internal/{apperror,httpx,validation,email,security,config,logger,health,database}/
internal/testutil/            testcontainers helpers, fakes, test-only sqlc output
internal/integration/         end-to-end HTTP tests
migrations/                   production schema (source of truth)
docs/                         GENERATED swagger
```

## Conventions

**Handlers are thin** — bind input (`httpx.BindBody`/`BindQuery`), parse path
params (`uuid.Parse(c.Params(...))`), extract device metadata where relevant
(`session.ExtractMetadata`), call exactly one service method, map the result
(`mapper.go`), return via `httpx.OK`/`OKWithMeta`/`Created`/`OKMessage`. Return
errors unchanged — never build an error response in a handler. No business logic,
no SQL, no direct DB access.

**Routes** — `func Routes(router fiber.Router, h *Handler)`. Auth is applied
per-route with `middleware.Auth(verifier)`, not `router.Use`.

**Services** — exported interface + unexported struct + `NewService` returning
`*service`, plus `...Result` structs. Dependencies are interfaces declared in the
consuming package's `types.go` (also what mocks are generated from). Keep
interfaces narrow (e.g. `session.SessionCreator`).

**Repositories** — consumer-defined `Repository` interface; unexported struct
holding `*xxxdb.Queries` plus `*pgxpool.Pool` when it needs a transaction;
`NewRepository` returns the interface. Pass sqlc `Params`/`Row` types straight
through; don't wrap them.

**Errors** (`internal/apperror`) — construct in the lowest layer that knows the
meaning. Repositories translate driver errors (`pgx.ErrNoRows` → `NotFoundWith`
or `UnauthorizedWith`, otherwise `Internal`); services and handlers pass errors
through untouched. Prefer the `...With(code, message, err)` form so clients get
a specific `error.code`. Responses come from `httpx` helpers: success is
`{"data","message","meta"}`, error is `{"error":{code,message,fields}}`. Don't
leak internal error text into `Message`.

**Transactions** — repositories hold `*pgxpool.Pool` and own their transaction
(`pool.Begin` → `queries.WithTx` → `defer tx.Rollback` → `tx.Commit`). There is
**no shared unit-of-work and no nested transaction across repositories** — if a
change seems to need one, ask rather than inventing a pattern. Use
`SELECT ... FOR UPDATE` for read-modify-write that must not race, and keep rules
that depend on locked rows inside the transaction.

## Auth and sessions

Security-critical. Leave unchanged unless the task is explicitly about auth.
Implementation lives in `internal/security` and `internal/api/auth/session`.

- Access token = HS256 JWT carrying `sub` (user) and `sid` (session); the verifier
  pins the algorithm and requires both claims.
- Refresh tokens are opaque and stored **hashed only**, rotated through a
  `replaced_by` link. **Reusing an already-replaced token must revoke the entire
  session** — never weaken or bypass this.
- Sessions have both idle and absolute expiry; either one revokes the session.
  Lifetimes are constants in `session/constants.go`.
- Passwords are bcrypt, verification codes HMAC'd with a configured secret. Never
  store or log plaintext secrets, tokens, or codes.
- Email flows are non-enumerating by design — don't change response shapes to
  reveal whether an account exists.
- Keep Google ID-token verification behind `login.GoogleTokenVerifier` so tests can fake it.
- Registration / password-reset verification endpoints are intentionally
  unauthenticated; the `verification_id` + emailed PIN is the credential.

## Data integrity

Partial unique indexes and CHECK constraints encode business rules (one active
registration or reset per email, one active verification code per request, one
continuation per registration, one user per email, one identity per
provider+subject). Names are listed in `internal/database/baseline/schema.sql`.
**Do not drop or weaken them to make a test pass** — fix the code, and when
adding an allowed value, do it in a migration.

## Migrations and baseline schema

Two schemas with different roles. **They can drift.**

- `migrations/` — production database schema and **source of truth**, as
  `NNNNNN_name.up.sql` / `.down.sql` pairs. **Never edit an existing migration**
  once created — add a new one instead.
- When a schema change is required, create a new migration with the project's
  migration CLI, following the existing convention:

  ```sh
  migrate create -ext sql -dir migrations -seq <migration_name>
  # e.g. migrate create -ext sql -dir migrations -seq remove_pending_social_display_name
  ```

- **Creating a migration and applying a migration are two separate operations.**
  Creating the files may be part of a task; applying them is not.
- **Never apply a migration to any database unless the user explicitly instructs
  it.** Don't run `migrate -path migrations -database "<database-url>" up` or any
  equivalent. No migration runner is wired into the app either.
- **Don't invent database URLs, environments, or migration targets** — applying is
  a user-controlled step on their environment.
- If a task requires a schema change, stop after creating the migration and report
  the files with their SQL/diff for review, unless the user asked for application as
  part of the task.
- `internal/database/baseline/schema.sql` — manually maintained snapshot,
  embedded via `//go:embed`, used as the `schema:` input for the **test-only**
  sqlc targets in `internal/testutil/db/*` and applied to throwaway Postgres
  containers. Not used at runtime.
- Creating a migration does **not** update the baseline. Update it only when asked,
  and don't assume the two are in sync. Preserve its existing formatting and
  organization.

## Code generation

Generated code is **output**. Never hand-edit it. Always change the source, then
regenerate. There is no Makefile or task runner, so every step here is manual.

Output paths: `internal/**/mocks/` (mockgen), `internal/api/**/generated/` (sqlc),
`internal/testutil/db/**/generated/` (sqlc, test-only), and `docs/` —
`docs.go`, `swagger.json`, `swagger.yaml` (swag).

- **mockgen** — mocks are generated from the Go interfaces a package consumes
  (see `types.go` / `service.go` in the feature). Source and destination depend on
  the module or interface, so there is no single project-wide command or output
  directory. If an interface changes, modify the source interface first, then
  regenerate the affected mock.
- **sqlc** — feature `queries.sql` files are the source. `sqlc.yaml` defines query
  sources, generated packages, output directories, and the `pgx/v5` database
  configuration. If a query changes, modify `queries.sql` first, then run
  `sqlc generate`. Change `sqlc.yaml` only when its configuration actually needs
  to change.

  Integration-test queries at `internal/testutil/db/<feature>/queries.sql` generate
  into the sibling `generated/` directory via the same config.

  Every target sets `omit_unused_structs: true`, so `models.go` contains only table
  structs referenced by that target's queries. Generated persistence models are
  therefore not automatically shared across features. When a feature's queries
  project only part of a table, the feature owns its projected type in `types.go`;
  keep sqlc `Params`/`Row` types generated.

  A generated model with a real consumer in another feature may be shared:
  `sessiondb.Session` is used by both `login` and `registration` because session
  queries project the whole table. Such sharing depends on the owning target's
  queries continuing to reference that model.

  Do **not** introduce a shared domain type merely to avoid similar structs;
  `saved_item.SavedItem` and `collection.SavedItem` are deliberately separate
  projections.

- **swag** — edit handler annotation comments, then regenerate with:
  `swag init -g cmd/api/main.go -parseInternal`.

Features live under `internal/api/` — currently `auth/registration`,
`auth/login`, `auth/session`, `auth/password_reset`, `saved_item`, `collection`,
and `user`. Follow the existing `sqlc.yaml` mapping for the relevant module.

`collection` acts on a saved item and mounts under the same `/saved-items` prefix
as `saved_item` (`PUT /saved-items/:id/collection`). Its module registers into
that prefix independently; both features' routes coexist.

## API documentation

Every handler carries a `// Name godoc` swag block (summary, description including
possible error codes, params, success/failure responses, route); the global
annotation and `BearerAuth` definition live on `main()` in `cmd/api/main.go`.
`internal/api/swagger/` holds documentation-only response shapes — never import it
from runtime code. Don't put hand-written project docs in `docs/`; those belong
under `documentation/`.

## Testing

- Unit tests are colocated with source as `_test.go`, named after the unit under
  test (`*_service_test.go`, `*_helpers_test.go`). Use `testify` and
  `go.uber.org/mock`; mock the consumer interface from `types.go` and regenerate
  mocks rather than editing them.
- Integration tests live in `internal/integration/`. A shared `TestMain` starts one
  Postgres 18 testcontainer, applies the baseline schema, builds the real app via
  `testutil.NewApp`, then tears down. One shared database means tests must not collide
  on data. **Docker is required.**
- Prefer asserting error codes over error strings, and service/repository tests
  over handler plumbing.

## Verify changes

```sh
go build ./...
go vet ./...
go test ./...
```

Report which verification commands you ran and whether they passed or failed.
**Never claim tests pass unless they were actually run.** If Docker is unavailable,
state that plainly when integration tests cannot run.

## Rules

- Inspect the code rather than guessing; prefer existing patterns over new ones.
- Don't change architecture, add dependencies, or alter API contracts or
  auth/session behavior without explicit instruction.
