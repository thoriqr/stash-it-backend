package collection

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// Routes registers the collection endpoints.
//
// The path is nested under /saved-items because the resource being acted on is a
// saved item: it is what gets filed into a collection. The router is supplied by
// the module, which mounts this feature under the existing /saved-items prefix.
//
// Auth is applied per route with middleware.Auth, never router.Use, so every
// route here is deliberate about requiring a Bearer token.
func Routes(
	router fiber.Router,
	handler *Handler,
	verifier *security.AccessTokenVerifier,
) {
	router.Put(
		"/:id/collection",
		middleware.Auth(verifier),
		handler.PutSavedItem,
	)
}
