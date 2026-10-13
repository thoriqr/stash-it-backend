package session

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func Routes(
	router fiber.Router,
	handler *Handler,
	verifier *security.AccessTokenVerifier,
	ipRateLimiter middleware.RateLimiter,
) {
	// Refresh is bounded per client address, and mounted in front of the handler
	// rather than inside the service for the reason every other limiter here is
	// mounted before its handler: a refused request must never reach the work it
	// guards. Here that means no rotation, no successor token and no write at all,
	// so there is no partial state to reconcile.
	//
	// It sits ahead of body binding too, so a body that does not parse is counted.
	// The cheapest request a caller can send is one that cannot succeed, and a
	// charge placed behind the binding would count only the requests that were
	// already well-formed.
	//
	// The address is resolved by the same middleware every other budget uses, from
	// the same configured CLIENT_IP_SOURCE and trusted-proxy allowlist. There is no
	// second place here where a forwarding header is read.
	refreshIPLimit := middleware.IPRateLimit(
		ipRateLimiter,
		refreshIPRateLimitNamespace,
		ratelimit.Policy{
			Max:    RefreshIPRateLimitMax,
			Window: RefreshIPRateLimitWindow,
		},
	)

	router.Post("/refresh", refreshIPLimit, handler.RefreshToken)

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

	router.Delete(
		"/sessions/:session_id",
		middleware.Auth(verifier),
		handler.RevokeSession,
	)
}
