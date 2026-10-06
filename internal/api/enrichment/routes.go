package enrichment

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// Routes registers the enrichment endpoints.
//
// The path is nested under /saved-items because the resource being acted on is a
// saved item: it is the item whose metadata is enriched. The router is supplied
// by the module, which mounts this feature under the existing /saved-items
// prefix alongside saved_item and collection.
//
// Auth is applied per route with middleware.Auth, never router.Use, so every
// route here is deliberate about requiring a Bearer token.
func Routes(
	router fiber.Router,
	handler *Handler,
	verifier *security.AccessTokenVerifier,
) {
	router.Post(
		"/:id/enrich",
		middleware.Auth(verifier),
		handler.Enrich,
	)
}
