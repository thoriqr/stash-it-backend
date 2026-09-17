package session

import (
	"github.com/gofiber/fiber/v3"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func Routes(
	router fiber.Router,
	handler *Handler,
	verifier *security.AccessTokenVerifier,
) {
	router.Post("/refresh", handler.RefreshToken)

	router.Post(
		"/logout",
		middleware.Auth(verifier),
		handler.Logout,
	)

	router.Get(
		"/sessions",
		middleware.Auth(verifier),
		handler.ListSessions,
	)
}