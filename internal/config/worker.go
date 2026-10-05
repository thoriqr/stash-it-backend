package config

import (
	"errors"
	"fmt"
	"os"

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
}

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

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return WorkerConfig{}, fmt.Errorf("DATABASE_URL is required")
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		return WorkerConfig{}, fmt.Errorf("REDIS_URL is required")
	}

	return WorkerConfig{
		AppEnv:      appEnv,
		DatabaseURL: databaseURL,
		RedisURL:    redisURL,
	}, nil
}