package integration_test

import (
	"context"
	"log"
	"os"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thoriqr/stash-it-backend/internal/testutil"
)

var (
	testApp  *fiber.App
	testPool *pgxpool.Pool
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := testutil.StartPostgres(ctx)
	if err != nil {
		log.Printf("failed to start postgres container: %v", err)
		os.Exit(1)
	}

	pool, err := testutil.NewPostgresPool(ctx, container)
	if err != nil {
		log.Printf("failed to create postgres pool: %v", err)

		if terminateErr := testutil.TerminatePostgres(ctx, container); terminateErr != nil {
			log.Printf("failed to terminate postgres container: %v", terminateErr)
		}

		os.Exit(1)
	}

	if err := testutil.ApplyBaseline(ctx, pool); err != nil {
		log.Printf("failed to apply baseline: %v", err)

		pool.Close()

		if terminateErr := testutil.TerminatePostgres(ctx, container); terminateErr != nil {
			log.Printf("failed to terminate postgres container: %v", terminateErr)
		}

		os.Exit(1)
	}

	testPool = pool
	testApp = testutil.NewApp(pool)

	code := m.Run()

	pool.Close()

	if err := testutil.TerminatePostgres(ctx, container); err != nil {
		log.Printf("failed to terminate postgres container: %v", err)

		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}