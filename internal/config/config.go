package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	AppEnv                 string
	DatabaseURL            string
	VerificationCodeSecret string
	AccessTokenSecret      string
	GoogleClientID         string

	// GoogleDesktopClientID is the audience of ID tokens issued to the Windows
	// desktop application.
	//
	// It is optional because a deployment without a desktop client still serves
	// the existing mobile and web flows. An absent value simply leaves the
	// verifier accepting only GoogleClientID; it is never replaced with a
	// default, so a missing desktop client cannot silently become a different
	// client's audience.
	GoogleDesktopClientID string

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

	// EmailSender is how outbound mail leaves the process.
	//
	// It is loaded and validated here rather than at the composition root so a
	// deployment that names a provider but cannot use it refuses to start. The
	// alternative is a process that serves every other route while every PIN
	// issuance fails, which looks like a working deployment until somebody tries
	// to register.
	EmailSender EmailSenderConfig

	// Sentry is the optional error reporter. An empty DSN disables it, which is
	// an ordinary state for local development and for a deployment that has not
	// created a Sentry project yet.
	Sentry SentryConfig

	// OEmbedSkipHosts are exact hostnames for which the enricher does not call
	// any oEmbed endpoint. The HTML path still runs. It is loaded here so a bad
	// entry is a startup error rather than a setting that silently matches
	// nothing.
	OEmbedSkipHosts []string

	// ClientEnrichmentHosts maps each exact hostname the client fetches and parses
	// itself to the platform its items are filed under. A save of one of these
	// hosts is not queued for the worker, and the enrich endpoint accepts the
	// metadata the client extracted instead of fetching the page. Empty disables
	// the feature.
	//
	// The platform half is part of the setting rather than a code constant, so
	// adding a host is an environment change: automatic organization files its
	// items under exactly this name.
	ClientEnrichmentHosts map[string]string

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

	// ClientReleaseSecret guards the internal client-release maintenance routes,
	// which publish, force and roll back client releases. No user credential
	// reaches them.
	//
	// Optional by design, like SessionCleanupSecret: an unset value leaves the
	// public release check working while the maintenance routes refuse every
	// request. It is never defaulted, because an invented secret would look
	// configured and would not be.
	ClientReleaseSecret string

	// OperatorPageAddr is the address the operator page listens on, and an empty value
	// disables it entirely.
	//
	// The page is not part of the API: it is a second Fiber app on its own listener,
	// serving HTML to one person. Binding to loopback is what keeps it off the network
	// — a reachable address is a mistake, not a deployment shape — but the address is
	// configuration rather than a constant because development needs it on a local
	// address while the VPS needs `127.0.0.1`.
	//
	// It is never defaulted. A default would start a surface nobody asked for, and the
	// value has to differ between a laptop and a server anyway.
	OperatorPageAddr string

	// OperatorPageSecret guards the operator page when it is reachable by anything but
	// its own machine.
	//
	// Optional, and empty in the ordinary case: on the VPS the page is reached over an
	// SSH tunnel to loopback, and SSH is the authentication. The secret exists so that
	// binding to a reachable address by mistake is not the same as publishing every
	// user's email address and every report.
	OperatorPageSecret string
}

func Load() (Config, error) {
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "development"
	}

	if err := loadEnvFile(appEnv); err != nil {
		return Config{}, err
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

	// Optional by design, like SessionCleanupSecret below. A desktop client is
	// one additional way into the same Google flow, not a dependency of the
	// existing one, so its absence must not stop the API from starting. No
	// default is supplied: an invented audience would be the ID of a client this
	// deployment does not own.
	googleDesktopClientID := os.Getenv("GOOGLE_DESKTOP_CLIENT_ID")

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

	// Optional for the same reason: an unset value closes the maintenance routes
	// by construction rather than stopping the API, and it is never given a
	// default that would look configured.
	clientReleaseSecret := os.Getenv("CLIENT_RELEASE_SECRET")

	// Optional, and empty disables the operator page entirely. The page serves HTML to
	// one person; a deployment that has not asked for it should not have a listener for
	// it, so this is the one surface in this file that is closed by being absent rather
	// than by refusing.
	operatorPageAddr := os.Getenv("OPERATOR_PAGE_ADDR")

	// Optional, and empty skips the check. On the VPS the page is reached over an SSH
	// tunnel to loopback, where SSH is the authentication; the secret covers the case
	// where the page is bound somewhere reachable instead.
	operatorPageSecret := os.Getenv("OPERATOR_PAGE_SECRET")

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

	emailSender, err := loadEmailSender(appEnv)
	if err != nil {
		return Config{}, err
	}

	sentry, err := loadSentry(appEnv)
	if err != nil {
		return Config{}, err
	}

	oembedSkipHosts, err := loadOEmbedSkipHosts()
	if err != nil {
		return Config{}, err
	}

	clientEnrichmentHosts, err := loadClientEnrichmentHosts()
	if err != nil {
		return Config{}, err
	}

	return Config{
		AppEnv:                 appEnv,
		DatabaseURL:            databaseURL,
		VerificationCodeSecret: verificationCodeSecret,
		AccessTokenSecret:      accessTokenSecret,
		GoogleClientID:         googleClientID,
		GoogleDesktopClientID:  googleDesktopClientID,
		RedisURL:               redisURL,
		ClientIPSource:         clientIPSource,
		TrustedProxies:         trustedProxies,
		TrustedProxyHeader:     trustedProxyHeader,

		PasswordWorkConcurrency: passwordWorkConcurrency,
		PasswordWorkWait:        passwordWorkWait,

		PINEmailBudget: pinEmailBudget,

		EmailSender: emailSender,

		Sentry: sentry,

		OEmbedSkipHosts: oembedSkipHosts,

		ClientEnrichmentHosts: clientEnrichmentHosts,

		SessionCleanupSecret: sessionCleanupSecret,
		ClientReleaseSecret:  clientReleaseSecret,
		OperatorPageAddr:     operatorPageAddr,
		OperatorPageSecret:   operatorPageSecret,
	}, nil
}
