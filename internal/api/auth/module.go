package auth

import (
	"context"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	logindb "github.com/thoriqr/stash-it-backend/internal/api/auth/login/generated"
	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	registration "github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	registrationdb "github.com/thoriqr/stash-it-backend/internal/api/auth/registration/generated"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/email"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// RateLimiter is what every rate-limited route in this module is guarded by.
//
// It is declared here rather than reused from a sub-feature because this package
// is the one that wires them together, and it is the union of what they need
// rather than any one of them. Registration only ever charges a budget, so its own
// interface is Allow alone; login charges a budget and hands one back when the
// work it guarded turned out to have succeeded, so its own interface adds Release.
//
// Declaring the union here means the sub-features keep the narrowest interface
// they actually use, and this one keeps the single Redis, the single script and
// the single connection behind all of them.
type RateLimiter interface {
	Allow(
		ctx context.Context,
		namespace string,
		subject string,
		policy ratelimit.Policy,
	) (ratelimit.Result, error)

	Release(
		ctx context.Context,
		namespace string,
		subject string,
	) (ratelimit.Result, error)
}

// PINEmailBudget is what every feature that sends mail is bounded by.
//
// It is the union of what registration and password reset consume, declared here
// rather than imported from either of them because this package is the one that
// wires them together and it is the only place that can guarantee they were given
// the *same* budget. Two features handed separate instances — or two separately
// configured ones — would each have their own ceiling, and the whole point of
// sharing is that there is one.
//
// It is distinct from RateLimiter above on purpose. That one counts request rates
// in fixed windows and is mounted in front of handlers; this one bounds how much
// mail leaves over a rolling day, and it is spent inside services where the
// recipient is finally known. Conflating them would put a message budget on a
// dimension that has no message in it.
type PINEmailBudget interface {
	registration.PINEmailBudget
	passwordreset.PINEmailBudget
}

func RegisterModule(
	app *fiber.App,
	pool *pgxpool.Pool,
	cfg config.Config,
	log *zap.Logger,
	emailSender email.Sender,
	googleTokenVerifier login.GoogleTokenVerifier,
	pinRateLimiter RateLimiter,
	pinEmailBudget PINEmailBudget,
	passwordHasher *security.PasswordHasher,
) {
	authRouter := app.Group("/auth")

	// Shared security dependencies
	//
	// passwordHasher arrives rather than being built here. The limiter it wraps is
	// process-wide and now has a second consumer outside this module — the user
	// profile's password endpoint — so constructing it once in cmd/api/main.go is
	// what keeps "one derivation at a time" meaning one. This package and
	// internal/api/user are siblings that share the hasher; neither builds one.
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte(cfg.VerificationCodeSecret),
	)

	accessTokenGenerator := security.NewAccessTokenGenerator(
		[]byte(cfg.AccessTokenSecret),
	)

	accessTokenVerifier := security.NewAccessTokenVerifier(
		[]byte(cfg.AccessTokenSecret),
	)

	// Session
	sessionQueries := sessiondb.New(pool)

	sessionRepository := session.NewRepository(
		sessionQueries,
		pool,
	)

	sessionService := session.NewService(
		sessionRepository,
		accessTokenGenerator,
		log,
	)

	sessionHandler := session.NewHandler(
		sessionService,
	)

	session.Routes(
		authRouter,
		sessionHandler,
		accessTokenVerifier,
		pinRateLimiter,
	)

	// Mounted on its own prefix rather than on the /auth group, because nothing
	// reaching this route is a user credential. An external scheduler calls it
	// with a shared secret in a header, which is a different kind of
	// authentication from the Bearer token every /auth route is guarded by, and
	// sharing a prefix would blur that distinction.
	//
	// cleanupSecret is passed through rather than read from the environment here,
	// so the composition root stays the only place configuration is resolved and
	// a test can build an app that either has a secret or does not.
	session.MaintenanceRoutes(
		app.Group("/internal/maintenance"),
		sessionHandler,
		cfg.SessionCleanupSecret,
	)

	// Registration
	registrationQueries := registrationdb.New(pool)

	registrationRepository := registration.NewRepository(
		pool,
		registrationQueries,
	)

	registrationService := registration.NewService(
		registrationRepository,
		sessionService,
		accessTokenGenerator,
		passwordHasher,
		verificationCodeHasher,
		emailSender,
		pinEmailBudget,
	)

	registrationHandler := registration.NewHandler(
		registrationService,
	)

	registration.Routes(
		authRouter,
		registrationHandler,
		pinRateLimiter,
	)

	// Login
	loginQueries := logindb.New(pool)

	loginRepository := login.NewRepository(
		pool,
		loginQueries,
	)

	loginService := login.NewService(
		loginRepository,
		sessionService,
		registrationService,
		googleTokenVerifier,
		passwordHasher,
		accessTokenGenerator,
		pinRateLimiter,
	)

	loginHandler := login.NewHandler(
		loginService,
	)

	login.Routes(
		authRouter,
		loginHandler,
		pinRateLimiter,
	)

	// Password reset
	passwordResetQueries := passwordresetdb.New(pool)

	passwordResetRepository := passwordreset.NewRepository(
		pool,
		passwordResetQueries,
	)

	passwordResetService := passwordreset.NewService(
		passwordResetRepository,
		passwordHasher,
		verificationCodeHasher,
		emailSender,
		pinEmailBudget,
	)

	passwordResetHandler := passwordreset.NewHandler(
		passwordResetService,
	)

	passwordreset.Routes(
		authRouter,
		passwordResetHandler,
		pinRateLimiter,
	)
}
