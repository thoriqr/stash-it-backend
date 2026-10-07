package testutil

import (
	"context"
	"fmt"
	"sync"

	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	rediscontainer "github.com/testcontainers/testcontainers-go/modules/redis"
)

// The Redis container is started at most once per test binary and only if
// something asks for it.
//
// The integration suite needs Postgres for nearly every test, and only the queue
// tests need Redis. Starting Redis in TestMain would make a second container a
// precondition for all of them, so it is started on first use and terminated by
// TerminateRedisForTests at the end of the run instead. A container that is never
// needed is never created.
const (
	redisImage = "redis:8-alpine"

	redisDatabase = 3
)

// redisForTests holds the single container and client shared by the queue tests.
var redisForTests struct {
	mutex     sync.Mutex
	container *rediscontainer.RedisContainer
	client    goredis.UniversalClient
	err       error
}

// StartRedisForTests returns a Redis client connected to a test container,
// starting the container on first use.
//
// The same client is returned for the rest of the run. Tests share it because
// they share one Asynq namespace anyway: separate connections would not make
// their queues separate.
func StartRedisForTests(
	ctx context.Context,
) (goredis.UniversalClient, error) {
	redisForTests.mutex.Lock()
	defer redisForTests.mutex.Unlock()

	if redisForTests.err != nil {
		return nil, redisForTests.err
	}

	if redisForTests.client != nil {
		return redisForTests.client, nil
	}

	container, err := rediscontainer.Run(ctx, redisImage)
	if err != nil {
		redisForTests.err = fmt.Errorf("starting redis container: %w", err)

		return nil, redisForTests.err
	}

	// The container's own database 0 is left alone and the tests use a separate
	// one, so flushing between tests can never touch anything else that happened
	// to connect to the same container.
	endpoint, err := container.Endpoint(ctx, "")
	if err != nil {
		redisForTests.err = fmt.Errorf("redis container endpoint: %w", err)

		return nil, redisForTests.err
	}

	host, port, err := splitHostPort(endpoint)
	if err != nil {
		redisForTests.err = err

		return nil, redisForTests.err
	}

	client := goredis.NewClient(&goredis.Options{
		Addr: fmt.Sprintf("%s:%s", host, port),
		DB:   redisDatabase,
	})

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		_ = testcontainers.TerminateContainer(container)

		redisForTests.err = fmt.Errorf("pinging redis container: %w", err)

		return nil, redisForTests.err
	}

	redisForTests.container = container
	redisForTests.client = client

	return client, nil
}

// TerminateRedisForTests stops the container if one was started, and is safe to
// call when none ever was.
//
// It takes the context rather than reaching for Background so the caller decides
// how long a teardown may take, matching the Postgres teardown beside it.
func TerminateRedisForTests(ctx context.Context) error {
	redisForTests.mutex.Lock()
	defer redisForTests.mutex.Unlock()

	if redisForTests.container == nil {
		return nil
	}

	if redisForTests.client != nil {
		if err := redisForTests.client.Close(); err != nil {
			return err
		}
	}

	container := redisForTests.container

	redisForTests.container = nil
	redisForTests.client = nil

	return testcontainers.TerminateContainer(container)
}
