package auth

import (
	"github.com/gofiber/fiber/v3"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func extractRegistrationContinuationToken(c fiber.Ctx) (string, error) {
	token := c.Get("X-Registration-Continuation")

	if token == "" {
		return "", apperror.UnauthorizedWith(
			CodeRegistrationContinuationRequired,
			"registration continuation token is required",
			nil,
		)
	}

	return token, nil
}