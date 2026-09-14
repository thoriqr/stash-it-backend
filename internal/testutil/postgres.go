package testutil

import (
	"context"

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

func StartPostgres(ctx context.Context) (*postgres.PostgresContainer, error) {
	return postgres.Run(
		ctx,
		postgresImage,
		postgres.WithDatabase(postgresDatabase),
		postgres.WithUsername(postgresUsername),
		postgres.WithPassword(postgresPassword),
		postgres.BasicWaitStrategies(),
	)
}

func NewPostgresPool(
	ctx context.Context,
	container *postgres.PostgresContainer,
) (*pgxpool.Pool, error) {
	connString, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, err
	}

	return pgxpool.New(ctx, connString)
}

func ApplyBaseline(
	ctx context.Context,
	pool *pgxpool.Pool,
) error {
	_, err := pool.Exec(ctx, string(baseline.Schema))
	return err
}

func TerminatePostgres(
	ctx context.Context,
	container *postgres.PostgresContainer,
) error {
	return testcontainers.TerminateContainer(container)
}