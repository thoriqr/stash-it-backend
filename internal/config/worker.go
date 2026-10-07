package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// WorkerConfig is the configuration cmd/worker loads. It is a separate type
// from Config on purpose: the worker is a separate binary and must be
// deployable with only the environment variables it actually reads, so it must
// never require API-only values such as Google OAuth credentials or the
// verification and access token secrets.
//
// Sharing an environment with the API is fine and is not what this type is
// separating. The boundary is the struct and the loader, so the worker
// validates only its own inputs.
type WorkerConfig struct {
	AppEnv string

	// DatabaseURL is carried now so the worker's configuration boundary is
	// settled before it needs the database. It is validated because a worker
	// that cannot reach Postgres later is misconfigured today, but no pool is
	// opened here: the worker has no database access yet.
	DatabaseURL string

	// RedisURL is a single connection URL rather than separate host, port, and
	// database fields, so the same value works for local Docker, Cloud Run, and
	// a VPS without the worker knowing which environment it is in.
	RedisURL string

	// WorkerConcurrency is how many tasks the worker processes at once.
	//
	// It is optional in the sense that an unset value is not an error: the default
	// applies. A value that is present but outside the allowed range is an error,
	// because silently replacing it would leave a deployment running at a
	// concurrency nobody asked for and nothing in the logs to say so.
	WorkerConcurrency int
}

// Worker concurrency bounds.
//
// Enrichment is network-bound: nearly all of an attempt is waiting on somebody
// else's server, bounded by the outbound fetch timeout. A small number of
// attempts at once therefore overlaps mostly waiting, while a large number only
// multiplies concurrent outbound requests and database connections against
// origins that are already slow.
//
// The range is a policy, not a safety limit, so it is stated here in one place
// and enforced by the loader rather than by the worker at startup.
const (
	// DefaultWorkerConcurrency is how many tasks run at once when the environment
	// does not say.
	DefaultWorkerConcurrency = 5

	// MinWorkerConcurrency is the smallest value that does any work. Asynq would
	// treat a non-positive value as "use the CPU count", which is the opposite of
	// what somebody asking for zero concurrency meant.
	MinWorkerConcurrency = 1

	// MaxWorkerConcurrency is the largest value that will be accepted. Beyond
	// this a worker is not doing more useful work; it is holding more outbound
	// requests and database connections open while the attempts behind them wait.
	MaxWorkerConcurrency = 20
)

// LoadWorker reads the worker configuration from the environment.
//
// It follows the same convention as Load: APP_ENV selects .env.<APP_ENV>, and
// the file is read from the working directory the same way. One deliberate
// difference is that a missing env file is not an error here. godotenv does not
// override variables that are already set, so a deployment that supplies real
// environment variables (Cloud Run, a systemd unit, a container) works without a
// checked-in or mounted file, while local development still picks up .env
// automatically. Config.Load keeps its stricter behavior for the API and was
// not changed.
func LoadWorker() (WorkerConfig, error) {
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "development"
	}

	envFile := fmt.Sprintf(".env.%s", appEnv)

	if err := godotenv.Load(envFile); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return WorkerConfig{}, fmt.Errorf(
			"failed to load environment file %q: %w",
			envFile,
			err,
		)
	}

	workerConcurrency, err := loadWorkerConcurrency()
	if err != nil {
		return WorkerConfig{}, err
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return WorkerConfig{}, fmt.Errorf("DATABASE_URL is required")
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		return WorkerConfig{}, fmt.Errorf("REDIS_URL is required")
	}

	return WorkerConfig{
		AppEnv:            appEnv,
		DatabaseURL:       databaseURL,
		RedisURL:          redisURL,
		WorkerConcurrency: workerConcurrency,
	}, nil
}

// loadWorkerConcurrency reads the concurrency setting from the environment.
//
// An unset value is not a mistake, so it resolves to DefaultWorkerConcurrency. A
// value that is present and wrong is a mistake, and it is reported as one rather
// than corrected: a typo, a zero where one was meant, or a number past the
// ceiling all mean the deployment says something the worker is not going to do.
// Failing here makes that visible instead of leaving a worker running at a
// concurrency nobody chose and nothing in the logs to explain it.
func loadWorkerConcurrency() (int, error) {
	raw := os.Getenv("WORKER_CONCURRENCY")
	if raw == "" {
		return DefaultWorkerConcurrency, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf(
			"WORKER_CONCURRENCY must be a whole number: %w",
			err,
		)
	}

	if value < MinWorkerConcurrency || value > MaxWorkerConcurrency {
		return 0, fmt.Errorf(
			"WORKER_CONCURRENCY must be between %d and %d, got %d",
			MinWorkerConcurrency,
			MaxWorkerConcurrency,
			value,
		)
	}

	return value, nil
}
