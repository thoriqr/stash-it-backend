package search

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// Routes registers the search endpoints.
//
// The router is supplied by the module, which mounts this feature under the
// /search prefix. Search is its own top level resource rather than a sub resource
// of saved items or collections, because one query spans both.
//
// Auth is applied per route with middleware.Auth, never router.Use, so every
// route here is deliberate about requiring a Bearer token. There is no public
// search route.
func Routes(
	router fiber.Router,
	handler *Handler,
	verifier *security.AccessTokenVerifier,
) {
	router.Get(
		"/",
		middleware.Auth(verifier),
		handler.Search,
	)
}
