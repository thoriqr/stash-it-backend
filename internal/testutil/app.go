package testutil

import (
	"github.com/gofiber/fiber/v3"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thoriqr/stash-it-backend/internal/api/auth"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/validation"
)

const testVerificationCodeSecret = "integration-test-verification-secret"

func NewApp(pool *pgxpool.Pool) *fiber.App {
	validate := validation.New()

	app := fiber.New(fiber.Config{
		ErrorHandler:   httpx.ErrorHandler,
		StructValidator: validate,
	})

	app.Use(recoverer.New())

	cfg := config.Config{
		VerificationCodeSecret: testVerificationCodeSecret,
	}

	auth.RegisterModule(
		app,
		pool,
		cfg,
	)

	return app
}