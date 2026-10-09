package saved_item

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
	router.Post(
		"/",
		middleware.Auth(verifier),
		handler.Create,
	)

	router.Get(
		"/:id",
		middleware.Auth(verifier),
		handler.Get,
	)

	router.Delete(
		"/:id",
		middleware.Auth(verifier),
		handler.Delete,
	)
}
