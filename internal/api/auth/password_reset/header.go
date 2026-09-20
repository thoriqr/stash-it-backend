package password_reset

import (
	"github.com/gofiber/fiber/v3"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func extractPasswordResetContinuationToken(c fiber.Ctx) (string, error) {
	token := c.Get("X-Password-Reset-Continuation")

	if token == "" {
		return "", apperror.UnauthorizedWith(
			CodePasswordResetContinuationRequired,
			"password reset continuation token is required",
			nil,
		)
	}

	return token, nil
}