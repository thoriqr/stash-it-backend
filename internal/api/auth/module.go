package auth

import (
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	registration "github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	registrationdb "github.com/thoriqr/stash-it-backend/internal/api/auth/registration/generated"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func RegisterModule(
	app *fiber.App,
	pool *pgxpool.Pool,
	cfg config.Config,
) {
	authRouter := app.Group("/auth")
	// Registration
	registrationQueries := registrationdb.New(pool)

	registrationRepository := registration.NewRepository(
		pool,
		registrationQueries,
	)

	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte(cfg.VerificationCodeSecret),
	)

	passwordHasher := security.NewPasswordHasher()

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

	// ============================================================
	// Login
	// ============================================================

	// loginQueries := logindb.New(pool)
	//
	// loginRepository := login.NewRepository(
	//     pool,
	//     loginQueries,
	// )
	//
	// loginService := login.NewService(
	//     loginRepository,
	//     ...,
	// )
	//
	// loginHandler := login.NewHandler(
	//     loginService,
	// )
	//
	// login.Routes(
	//     authRouter,
	//     loginHandler,
	// )

	// ============================================================
	// Session
	// ============================================================

	// sessionQueries := sessiondb.New(pool)
	//
	// sessionRepository := session.NewRepository(
	//     pool,
	//     sessionQueries,
	// )
	//
	// sessionService := session.NewService(
	//     sessionRepository,
	//     ...,
	// )
	//
	// sessionHandler := session.NewHandler(
	//     sessionService,
	// )
	//
	// session.Routes(
	//     authRouter,
	//     sessionHandler,
	// )
}