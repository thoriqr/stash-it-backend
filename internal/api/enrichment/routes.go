package enrichment

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
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
	userRateLimiter middleware.RateLimiter,
) {
	// This is the only route in the application that performs an outbound fetch
	// inside the API process, so its budget is the tightest one here and it is
	// mounted between Auth and the handler: a refused request must never fetch a
	// page, and must never write enrichment state.
	//
	// It runs after Auth because the budget belongs to the account. The identity is
	// already verified, and it is the thing every query on this path is scoped by.
	enrichUserLimit := middleware.UserRateLimit(
		userRateLimiter,
		enrichUserRateLimitNamespace,
		ratelimit.Policy{
			Max:    EnrichUserRateLimitMax,
			Window: EnrichUserRateLimitWindow,
		},
	)

	router.Post(
		"/:id/enrich",
		middleware.Auth(verifier),
		enrichUserLimit,
		handler.Enrich,
	)
}
