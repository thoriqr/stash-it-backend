package testutil

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/thoriqr/stash-it-backend/internal/database/baseline"
)

const (
	postgresImage    = "postgres:18"
	postgresDatabase = "stash_it_test"
	postgresUsername = "stash_it_test"
	postgresPassword = "stash_it_test"
)

func NewPostgres(t *testing.T) *postgres.PostgresContainer {
	t.Helper()

	ctx := context.Background()

	container, err := postgres.Run(
		ctx,
		postgresImage,
		postgres.WithDatabase(postgresDatabase),
		postgres.WithUsername(postgresUsername),
		postgres.WithPassword(postgresPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}

	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Errorf("failed to terminate postgres container: %v", err)
		}
	})

	return container
}

func NewPostgresPool(
	t *testing.T,
	container *postgres.PostgresContainer,
) *pgxpool.Pool {
	t.Helper()

	ctx := context.Background()

	connString, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("failed to get postgres connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("failed to create postgres pool: %v", err)
	}

	t.Cleanup(pool.Close)

	return pool
}

func ApplyBaseline(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	ctx := context.Background()

	if _, err := pool.Exec(ctx, string(baseline.Schema)); err != nil {
		t.Fatalf("failed to apply baseline schema: %v", err)
	}
}