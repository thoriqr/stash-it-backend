package auth

import (
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	logindb "github.com/thoriqr/stash-it-backend/internal/api/auth/login/generated"
	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	registration "github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	registrationdb "github.com/thoriqr/stash-it-backend/internal/api/auth/registration/generated"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func RegisterModule(
	app *fiber.App,
	pool *pgxpool.Pool,
	cfg config.Config,
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

	// Registration

	registrationQueries := registrationdb.New(pool)

	registrationRepository := registration.NewRepository(
		pool,
		registrationQueries,
	)

	registrationService := registration.NewService(
		registrationRepository,
		passwordHasher,
		verificationCodeHasher,
	)

	registrationHandler := registration.NewHandler(
		registrationService,
	)

	registration.Routes(
		authRouter,
		registrationHandler,
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

	// Login

	loginQueries := logindb.New(pool)

	loginRepository := login.NewRepository(
		loginQueries,
	)

	loginService := login.NewService(
		loginRepository,
		sessionService,
		passwordHasher,
		accessTokenGenerator,
	)

	loginHandler := login.NewHandler(
		loginService,
	)

	login.Routes(
		authRouter,
		loginHandler,
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
	)

	passwordResetHandler := passwordreset.NewHandler(
		passwordResetService,
	)

	passwordreset.Routes(
		authRouter,
		passwordResetHandler,
	)
}