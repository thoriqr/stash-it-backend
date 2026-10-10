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

func RegisterModule(
	app *fiber.App,
	pool *pgxpool.Pool,
	cfg config.Config,
	log *zap.Logger,
	emailSender email.Sender,
	googleTokenVerifier login.GoogleTokenVerifier,
	pinRateLimiter RateLimiter,
) {
	authRouter := app.Group("/auth")

	// Shared security dependencies
	passwordHasher := security.NewPasswordHasher()

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
	)

	sessionHandler := session.NewHandler(
		sessionService,
	)

	session.Routes(
		authRouter,
		sessionHandler,
		accessTokenVerifier,
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
		pinRateLimiter,
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
	)

	passwordResetHandler := passwordreset.NewHandler(
		passwordResetService,
	)

	passwordreset.Routes(
		authRouter,
		passwordResetHandler,
	)
}
