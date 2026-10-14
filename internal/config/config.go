package config

import (
	"fmt"
	"os"
	"time"

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

	// ClientIPSource, TrustedProxies and TrustedProxyHeader describe where the
	// client address comes from.
	//
	// They are loaded and validated here rather than at the composition root so
	// that a deployment which cannot establish a trustworthy client address
	// refuses to start. Every request limiter keys on that address, so a
	// configuration that resolved it wrongly would silently group unrelated
	// callers together, and nothing in the responses would say so.
	ClientIPSource     ClientIPSource
	TrustedProxies     []string
	TrustedProxyHeader string

	// PasswordWorkConcurrency is how many password derivations may run at once,
	// and PasswordWorkWait how long a caller waits for capacity before being
	// refused.
	//
	// They are loaded and validated here rather than at the composition root so
	// that a deployment whose values are unusable refuses to start. The process
	// can be running perfectly well with the defaults, so a value that is present
	// and wrong would otherwise be discovered as a lockout rather than as a
	// configuration error.
	PasswordWorkConcurrency int
	PasswordWorkWait        time.Duration

	// PINEmailBudget bounds how many verification PIN emails may be issued, per
	// address and in total, over a rolling window.
	//
	// It is loaded and validated here for the same reason as the values above and
	// for one more: the global figure is chosen to stay under a delivery
	// provider's plan quota, and that quota belongs to an account rather than to
	// this repository. It changes when the plan does, and it must not change by
	// requiring a release.
	PINEmailBudget PINEmailBudget

	// SessionCleanupSecret guards the internal session-cleanup endpoint, which an
	// external scheduler calls and which no user credential reaches.
	//
	// It is optional where the other secrets here are required, and the difference
	// is deliberate rather than an oversight. Those five are required because
	// nothing in the application works without them. This one guards a maintenance
	// operation that simply does not run: an unset value leaves the API serving
	// every other route exactly as before while the cleanup route refuses every
	// request, so a deployment that has not finished configuring a scheduler is
	// not stopped from starting.
	//
	// It is the same nil-able capability the saved item enqueuer models: "this
	// process does not do that for nothing" is an ordinary state, not a
	// misconfiguration. The route still exists when it is unset and answers 401,
	// because a route that cannot be distinguished from a wrong secret is a route
	// that tells an unauthenticated caller whether the feature is enabled.
	SessionCleanupSecret string
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

	// Optional by design, and deliberately not checked. An absent value is an
	// ordinary deployment that has not configured a scheduler yet, and the
	// cleanup route refuses every request while it is absent. Requiring it would
	// stop this application from starting over a maintenance operation it can
	// perfectly well live without, and giving it a default would ship a secret
	// that looks configured and is not.
	sessionCleanupSecret := os.Getenv("SESSION_CLEANUP_SECRET")

	clientIPSource, trustedProxies, trustedProxyHeader, err := loadClientIP()
	if err != nil {
		return Config{}, err
	}

	passwordWorkConcurrency, passwordWorkWait, err := loadPasswordWork()
	if err != nil {
		return Config{}, err
	}

	pinEmailBudget, err := loadPINEmailBudget()
	if err != nil {
		return Config{}, err
	}

	return Config{
		AppEnv:                 appEnv,
		DatabaseURL:            databaseURL,
		VerificationCodeSecret: verificationCodeSecret,
		AccessTokenSecret:      accessTokenSecret,
		GoogleClientID:         googleClientID,
		RedisURL:               redisURL,
		ClientIPSource:         clientIPSource,
		TrustedProxies:         trustedProxies,
		TrustedProxyHeader:     trustedProxyHeader,

		PasswordWorkConcurrency: passwordWorkConcurrency,
		PasswordWorkWait:        passwordWorkWait,

		PINEmailBudget: pinEmailBudget,

		SessionCleanupSecret: sessionCleanupSecret,
	}, nil
}
