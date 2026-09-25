package testutil

import (
	"context"

	"github.com/gofiber/fiber/v3"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thoriqr/stash-it-backend/internal/api/auth"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/validation"
)

const testVerificationCodeSecret = "integration-test-verification-secret"

type fakeGoogleTokenVerifier struct {
	identity login.GoogleIdentity
	err      error
}

func (f *fakeGoogleTokenVerifier) Verify(
	ctx context.Context,
	idToken string,
) (login.GoogleIdentity, error) {
	return f.identity, f.err
}

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

	googleTokenVerifier := &fakeGoogleTokenVerifier{
	identity: login.GoogleIdentity{
		Subject:       "google-subject-123",
		Email:         "google@example.com",
		EmailVerified: true,
		DisplayName:   "Google User",
	},
	}

	auth.RegisterModule(
		app,
		pool,
		cfg,
		googleTokenVerifier,
	)

	return app
}