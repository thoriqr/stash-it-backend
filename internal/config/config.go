package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	AppEnv                 string
	DatabaseURL            string
	VerificationCodeSecret string
	AccessTokenSecret      string
	GoogleClientID         string

	// RedisURL is the queue connection the API enqueues background work on.
	//
	// It is the same value the worker reads and is required on both sides, because
	// a task can only be processed by a worker pointed at the same Redis. It is
	// loaded here rather than read from the environment at the composition root so
	// that a missing value is a startup failure rather than a feature that quietly
	// stops being wired.
	RedisURL string
}

func Load() (Config, error) {
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "development"
	}

	envFile := fmt.Sprintf(".env.%s", appEnv)

	if err := godotenv.Load(envFile); err != nil {
		return Config{}, fmt.Errorf(
			"failed to load environment file %q: %w",
			envFile,
			err,
		)
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	verificationCodeSecret := os.Getenv("VERIFICATION_CODE_SECRET")
	if verificationCodeSecret == "" {
		return Config{}, fmt.Errorf(
			"VERIFICATION_CODE_SECRET is required",
		)
	}

	accessTokenSecret := os.Getenv("ACCESS_TOKEN_SECRET")
	if accessTokenSecret == "" {
		return Config{}, fmt.Errorf("ACCESS_TOKEN_SECRET is required")
	}

	googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
	if googleClientID == "" {
		return Config{}, fmt.Errorf("GOOGLE_CLIENT_ID is required")
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		return Config{}, fmt.Errorf("REDIS_URL is required")
	}

	return Config{
		AppEnv:                 appEnv,
		DatabaseURL:            databaseURL,
		VerificationCodeSecret: verificationCodeSecret,
		AccessTokenSecret:      accessTokenSecret,
		GoogleClientID:         googleClientID,
		RedisURL:               redisURL,
	}, nil
}