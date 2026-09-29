package testutil

import (
	"context"

	"github.com/gofiber/fiber/v3"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thoriqr/stash-it-backend/internal/api/auth"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/email"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/logger"
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

type FakeEmailSender struct {
	Messages []email.Message
}

func (s *FakeEmailSender) Send(
	ctx context.Context,
	message email.Message,
) error {
	s.Messages = append(s.Messages, message)
	return nil
}

func NewApp(pool *pgxpool.Pool) (*fiber.App, *FakeEmailSender) {
	validate := validation.New()

	log, err := logger.New("development")
	if err != nil {
		panic(err)
	}

	app := fiber.New(fiber.Config{
		ErrorHandler:   httpx.NewErrorHandler(log),
		StructValidator: validate,
	})

	app.Use(recoverer.New())
	app.Use(requestid.New())

	cfg := config.Config{
		AppEnv:                 "development",
		VerificationCodeSecret: testVerificationCodeSecret,
	}

	emailSender := &FakeEmailSender{}

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
		log,
		emailSender,
		googleTokenVerifier,
	)

	return app, emailSender
}