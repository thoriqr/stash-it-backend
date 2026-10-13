package search

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
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
	h *Handler,
	verifier *security.AccessTokenVerifier,
	userRateLimiter middleware.RateLimiter,
) {
	// Search is bounded per authenticated user, and mounted between Auth and the
	// handler for the reason every other user budget in this application is
	// mounted there: the subject is the verified identity and there is nothing to
	// key on before Auth has run, and a refused request must never reach the scan
	// it guards. A refusal performs no query at all, so there is no partial state
	// to reconcile.
	//
	// It is keyed on the account rather than on an address because the work is
	// proportional to the rows that account owns. See SearchUserRateLimitMax for
	// what that costs and for why nothing else bounds it.
	searchUserLimit := middleware.UserRateLimit(
		userRateLimiter,
		searchUserRateLimitNamespace,
		ratelimit.Policy{
			Max:    SearchUserRateLimitMax,
			Window: SearchUserRateLimitWindow,
		},
	)

	router.Get(
		"/",
		middleware.Auth(verifier),
		searchUserLimit,
		h.Search,
	)
}
