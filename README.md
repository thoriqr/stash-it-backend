# Stash It Backend

Go API and worker for Stash It.

## Requirements

- Go 1.27 (match `go.mod`).
- Docker Desktop or Docker Engine, for local PostgreSQL/Redis and integration
  tests.
- `migrate` CLI for database migrations.
- Optional: `sqlc` and `swag` when changing generated code.

## Local development

### 1. Start PostgreSQL and Redis

```sh
docker compose -f docker-compose.dev.yml up -d
```

### 2. Create `.env.development`

```powershell
Copy-Item .env.example .env.development
```

Fill in the values.

### 3. Apply migrations

```sh
migrate -path migrations \
  -database "postgres://stash_it_dev:stash_it_dev@localhost:5432/stash_it_dev?sslmode=disable" \
  up
```

### 4. Run the API

```sh
go run ./cmd/api
```

- API: `http://localhost:8080`
- Swagger: `http://localhost:8080/docs`

### 5. Run the worker

```sh
go run ./cmd/worker
```

## Commands

```sh
go build ./...
go vet ./...
go test ./...                                # integration tests need Docker
go test -race ./path/to/pkg                  # needs cgo and a C compiler
sqlc generate                                # after editing queries.sql
swag init -g cmd/api/main.go -parseInternal  # after editing handler annotations
migrate create -ext sql -dir migrations -seq <migration_name>
```

Deployable binaries:

```sh
go build -o build/stash-it-api ./cmd/api
go build -o build/stash-it-worker ./cmd/worker
```

## Tests

Unit tests are colocated with source as `_test.go`. Integration tests live in
`internal/integration/` and require Docker because the shared `TestMain` starts
PostgreSQL and, when needed, Redis containers.

## Documentation

- [AGENTS.md](AGENTS.md) — architecture, conventions and invariants.
- [documentation/](documentation/) — design decisions and operational reference.
- [docs/](docs/) — generated Swagger.
- [documentation/deployment.md](documentation/deployment.md) — deployment
  topology, configuration and operations.
