# AGENTS.md

Operating guide for coding agents. The existing implementation is the source of
truth — when this file and the code disagree, the code wins. Inspect the relevant
code and tests rather than guessing. Update this file only if explicitly asked.

## Identity

- Module: `github.com/thoriqr/stash-it-backend`.
- Go version: `1.27.0`; match `go.mod` and do not bump it.
- HTTP framework: Fiber v3. Handlers use `func(c fiber.Ctx) error`;
  `fiber.Ctx` is a value type.
- Database: PostgreSQL through `pgx/v5` and `pgxpool` directly. No ORM or query
  builder.
- API documentation: Swagger 2.0 through `swaggo/swag`, served at `/docs/*`.
- Logging: `zap`. Validation: `validator/v10`.
- Project progress and current implementation status belong in `PROGRESS.md`.

## Repository structure

The project uses vertical slices by feature. Each feature owns its application
stack. There is no DI container; dependencies are wired explicitly in
`module.go`.

```text
cmd/
  api/main.go                 API bootstrap; registers feature modules
  worker/main.go              Worker bootstrap; registers task handlers

internal/
  api/<module>/               Feature HTTP layer and application stack:
                              routes.go, handler.go, service.go,
                              service_validation.go, repository.go,
                              mapper.go, request.go, response.go,
                              error_codes.go, constants.go, types.go,
                              generated/, mocks/
  api/auth/module.go          Composition root for auth sub-features
  middleware/                 HTTP middleware, including auth and rate limiting
  apperror/                   Application error types
  httpx/                      HTTP binding and response helpers
  validation/                 Shared validation
  email/                      Email functionality
  security/                   Security-sensitive primitives and boundaries
  config/                     Configuration
  logger/                     Logging setup
  health/                     Health functionality
  database/                   Database setup and test baseline
  ratelimit/                  Redis-backed rate-limit implementation
  enrichment/                 Reusable metadata extraction
  worker/
    queue/                    Background task contract shared by API and worker
    <feature>/                Worker handler, service, repository,
                              generated/, mocks/
  testutil/                   Testcontainers helpers, fakes, test-only sqlc output
  integration/                End-to-end HTTP and worker tests

migrations/                   Production database schema; source of truth
docs/                         Generated Swagger documentation
documentation/                Hand-written project documentation
```

This is an orientation map, not an exhaustive package listing. Inspect the
relevant feature before deciding where a change belongs. Do not introduce a new
architectural pattern when an existing feature already provides the appropriate
one.

## Conventions

### Handlers and routes

- Keep handlers thin. Bind input with `httpx.BindBody` or `httpx.BindQuery`,
  parse path parameters with `uuid.Parse(c.Params(...))`, extract device
  metadata with `session.ExtractMetadata` where relevant, call the appropriate
  service method, map the result in `mapper.go`, and return through the shared
  `httpx` helpers (`OK`, `OKWithMeta`, `Created`, or `OKMessage`).
- Return service errors unchanged. Do not put business logic, SQL, or direct
  database access in handlers.
- Define routes with `func Routes(router fiber.Router, h *Handler)`.
- Apply authentication to each protected route with
  `middleware.Auth(verifier)`. Do not apply it globally with `router.Use`.

### Services and dependencies

- Use an exported service interface with an unexported implementation.
- `NewService` returns `*service`; use `...Result` structs where appropriate.
- Keep dependencies narrow. Declare interfaces in the consuming package's
  `types.go` and generate mocks from those interfaces.
- Follow the existing constructor and explicit wiring patterns. Do not add a
  DI container or service locator.

### Repositories and SQL

- Use a consumer-defined `Repository` interface.
- Follow the existing repository implementation pattern: an unexported struct
  holding `*xxxdb.Queries` and `*pgxpool.Pool` when a transaction is needed;
  `NewRepository` returns the interface.
- Pass sqlc `Params` and `Row` types through directly. Do not wrap them without
  a concrete reason.
- Keep SQL and direct database access in repositories, not handlers.
- Preserve existing ownership checks and database constraints. A resource ID
  or foreign key alone does not establish user ownership.

### Errors and responses

- Use `internal/apperror` for application errors.
- Construct an error in the lowest layer that knows its meaning.
- Repositories translate driver errors (for example, `pgx.ErrNoRows` to the
  appropriate not-found or unauthorized error); services and handlers pass
  errors through.
- Prefer `...With(code, message, err)` when retaining an underlying error.
- Do not expose internal error text in API `Message`.
- Use the shared `httpx` response helpers rather than creating another response
  envelope or helper pattern.

### Transactions and concurrency

- Repositories own their transactions:
  `pool.Begin` → `queries.WithTx` → `defer tx.Rollback` → `tx.Commit`.
- There is no shared unit-of-work and no nested transaction across repositories.
  If a change appears to require one, ask rather than inventing a pattern.
- Follow the transaction and locking patterns of the relevant feature; do not
  change concurrency behavior as incidental cleanup.

## Security-sensitive code

Security-critical behavior must not be weakened as incidental cleanup or
refactoring. Leave authentication and session behavior unchanged unless the
task explicitly concerns auth.

- Authentication and session implementation lives in `internal/security` and
  `internal/api/auth/session`.
- Read `documentation/auth-sessions.md` before changing token rotation, reuse
  detection, revocation, or session expiry.
- Never store or log plaintext secrets, tokens, passwords, or verification codes.
- Preserve the existing password hashing, token validation, refresh-token
  rotation and reuse detection, session expiry/revocation, account-linking, and
  rate-limit policies.
- Keep Google ID-token verification behind `login.GoogleTokenVerifier`.
- Preserve the existing separation between rate-limit namespaces and the
  existing shared password-work capacity limit.

### Outbound requests and enrichment

- `internal/security` is the outbound-fetch boundary. Use
  `security.NewGuardedHTTPClient(policy)` for user-supplied URLs; do not add a
  second outbound client, transport, or proxy path.
- Keep the existing scheme, port, address-validation, and proxy policies in
  `internal/security`; do not duplicate them elsewhere.
- `internal/enrichment` receives its `*http.Client` through constructor
  injection. It must not construct a client, use `http.DefaultClient`, or call
  `http.Get`.
- Keep `internal/enrichment` independent of Fiber, `internal/api`, sqlc, Asynq,
  and persistence.
- The synchronous API integration and background worker reuse the same
  `internal/enrichment` capability. Do not create a second extraction layer.

## Background workers

- `cmd/worker` is a standalone binary with no HTTP surface. It must not import
  `internal/api`; its persistence and application code lives under
  `internal/worker`.
- `internal/worker/queue` is the contract between the API and worker binaries.
  Keep it free of Fiber, `internal/api`, and persistence.
- `internal/api/saved_item` uses its own one-method enqueuer interface rather
  than depending directly on the queue package.
- Preserve the existing save-before-enqueue ordering.
- Reuse `internal/enrichment` and `internal/security` rather than introducing
  alternate implementations.
- Keep retry classification in `enrichment.Kind`; the worker maps that
  classification to the queue decision.
- Never log a full Redis URL. Log its parsed address and database instead.

## Database, migrations, and data integrity

- `migrations/` is the production schema source of truth.
- Never edit an existing migration after it has been created; add a new
  migration instead.
- Creating and applying a migration are separate operations.
- Never apply a migration to any database unless the user explicitly instructs
  you to do so. Do not run `migrate ... up` or an equivalent command.
- Do not invent database URLs, environments, or migration targets.
- `internal/database/baseline/schema.sql` is a manually maintained test
  baseline. It is separate from production migrations and is not updated by
  creating a migration.
- Update the test baseline only when asked, preserving its formatting and
  organization.
- Database constraints encode business rules. Do not drop or weaken them to
  make a test pass; fix the code.

## Generated code and API documentation

Generated code is output, not an editing target. Change the source first, then
regenerate.

- Never hand-edit generated mocks, sqlc output, or Swagger output.
- Generate mocks from the interfaces consumed by the package.
- sqlc query files are the source. `sqlc.yaml` defines targets, packages,
  output directories, and pgx configuration. Run `sqlc generate` after query
  changes.
- Integration-test queries live under
  `internal/testutil/db/<feature>/queries.sql` and generate into the sibling
  `generated/` directory.
- Follow the existing sqlc target mapping. Do not introduce shared domain types
  merely to avoid similar generated structs.
- Swagger annotations belong with handlers. Update the annotations and
  regenerate the specification when they change:

  ```sh
  swag init -g cmd/api/main.go -parseInternal
  ```

- Generated Swagger belongs in `docs/`. Hand-written project documentation
  belongs in `documentation/`.
- `internal/api/swagger/` contains documentation-only response shapes. Never
  import it from runtime code.
- Swagger is the reference for API documentation and contracts.

## Testing and verification

- Unit tests are colocated with source as `_test.go`, following the existing
  naming conventions.
- Use `testify` and `go.uber.org/mock`. Mock consumer interfaces from
  `types.go` and regenerate mocks rather than editing them.
- Integration tests live in `internal/integration/`.
- The shared integration `TestMain` starts one PostgreSQL testcontainer,
  applies the baseline schema, builds the real app through `testutil.NewApp`,
  and tears it down. Worker tests start a Redis testcontainer lazily when
  needed. Docker is required.
- Integration tests share the database and Redis queue namespace. Tests must
  not collide on either.
- Prefer asserting error codes over error strings.
- Test business behavior at the service/repository level rather than testing
  handler plumbing.
- Run `go test -race` for packages whose concurrent code is added or changed.
  It requires cgo and a C compiler. Repeated ordinary test runs are not a
  substitute for the race detector.

For normal backend changes, run:

```sh
go build ./...
go vet ./...
go test ./...
```

Run relevant generators when their source files change. If Docker or another
requirement prevents a check from running, state that plainly. Report only
commands actually executed and their results; never claim an unrun check passed.

## Before finishing

- Review the diff for unrelated changes and accidental generated-file edits.
- Confirm that architecture, API contracts, and auth/session behavior changed
  only when explicitly requested.
- Summarize the changes and important trade-offs.
- List tests, generators, and verification commands actually run, including
  checks that could not be run and why.

## Rules

- Inspect the code rather than guessing; prefer existing patterns over new ones.
- Keep changes scoped to the requested task; do not perform unrelated refactors.
- Do not add dependencies without explicit instruction.
- Do not change architecture, public API contracts, or auth/session behavior
  without explicit instruction.
- Never manually edit generated files.
- Never apply database migrations without explicit instruction.
- If a requested change conflicts with an existing architectural or security
  invariant, stop and explain the conflict before implementing a workaround.
- Do not update this `AGENTS.md` unless explicitly asked.
- Write comments only when they provide meaningful context that cannot be
  readily inferred from the code, such as design decisions, constraints, or
  non-obvious behavior.
- Keep comments concise and focused on explaining why something is necessary;
  avoid narrating the code, repeating implementation details, or adding
  redundant explanations. Longer comments are appropriate when necessary to
  explain critical or complex behavior, constraints, or design decisions.
