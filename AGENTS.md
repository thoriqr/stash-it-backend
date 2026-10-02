# AGENTS.md

Operating guide for coding agents. The existing implementation is the source of
truth — when this file and the code disagree, the code wins. Update this file only
if explicitly asked.

## Identity

- Module `github.com/thoriqr/stash-it-backend`, Go `1.27.0` (match `go.mod`; don't bump).
- Fiber **v3**; handlers are `func(c fiber.Ctx) error` — `fiber.Ctx` is a **value** type.
- Postgres via `pgx/v5` + `pgxpool` directly. No ORM, no query builder.
- Swagger 2.0 via `swaggo/swag`, served at `/docs/*`. `zap` logging, `validator/v10` validation.
- Scope today: authentication (`internal/api/auth`). `internal/api/user` has a
  repository and generated code but no service/handler/routes — scaffolding, not dead code.

## Architecture

Vertical slices by feature; each owns its whole stack. **No DI container** —
wiring is explicit, by hand, in `module.go`.

```
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
through untouched. Prefer the `...With(code, message, err)` form so clients get a
specific `error.code`. Responses come from `httpx` helpers: success is
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

- `migrations/` — production schema and source of truth, as
  `NNNNNN_name.up.sql` / `.down.sql` pairs. Never edit existing migrations, never
  create one unless asked, and **never run or apply migrations unless explicitly
  instructed** (no runner is wired into the app).
- `internal/database/baseline/schema.sql` — manually maintained snapshot,
  embedded via `//go:embed`, used as the `schema:` input for the **test-only**
  sqlc targets in `internal/testutil/db/*` and applied to throwaway Postgres
  containers. Not used at runtime.
- Adding a migration does **not** update the baseline. Update it only when asked,
  and don't assume the two are in sync. Preserve its existing formatting and
  organization.

## Generated code — never hand-edit

- `internal/api/**/generated/` and `internal/testutil/db/**/generated/` (sqlc)
- `internal/**/mocks/` (gomock)
- `docs/` (`docs.go`, `swagger.json`, `swagger.yaml`)

There is no Makefile or task runner, so regeneration is manual: `sqlc generate`,
and `swag init -g cmd/api/main.go -o docs`. Edit `queries.sql` and the swag
annotations — never the output.

## API documentation

Every handler carries a `// Name godoc` swag block (summary, description including
possible error codes, params, success/failure responses, route); the global
annotation and `BearerAuth` definition live on `main()` in `cmd/api/main.go`.
`internal/api/swagger/` holds documentation-only response shapes — never import it
from runtime code. Don't put hand-written project docs in `docs/`; those belong
under `documentation/` (not yet created).

## Testing

- Unit tests are colocated with source as `_test.go`, named after the unit under
  test (`*_service_test.go`, `*_helpers_test.go`). Use `testify` and
  `go.uber.org/mock`; mock the consumer interface from `types.go` and regenerate
  mocks rather than editing them.
- Integration tests live in `internal/integration/`. A single shared `TestMain`
  starts one Postgres 18 testcontainer, applies the baseline schema, builds the
  real app via `testutil.NewApp`, then tears down. One shared database means tests
  must not collide on data. **Docker is required.**
- Prefer asserting error codes over error strings, and service/repository tests
  over handler plumbing.

## Verify changes

```sh
go build ./...
go vet ./...
go test ./...
```

CI runs `go test ./... -v` on push and PR without provisioning Docker, so the
integration suite needs a Docker-enabled runner or a local run. Report which
commands you ran and what they printed. Never claim tests pass without having run
them; say so plainly if Docker is unavailable.

## Rules

- Inspect the code rather than guessing; prefer existing patterns over new ones.
- Don't change architecture, add dependencies, or alter API contracts and
  auth/session behavior without explicit instruction.
- Don't modify migration files or `internal/database/baseline/schema.sql` unless
  explicitly instructed, and don't apply migrations unless explicitly instructed.
- Don't hand-edit generated code.
