package session

import (
	"crypto/subtle"

	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
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

// cleanupGuard is the authentication for the internal maintenance routes.
//
// It is kept here rather than in internal/middleware because this is the only
// caller of it. Putting a one-consumer abstraction in the shared middleware
// package would give every future feature a guard to reach for and an import to
// add, for a policy that is specific to these routes: they are not authenticated
// as a user, they hold no user credential at all, so none of the concepts
// middleware already models — a Bearer token, a rate-limit budget, a resolved
// client address — apply to them.
//
// The secret is compared with subtle.ConstantTimeCompare, matching
// internal/security, which uses it for password verification. The comparison is
// not a meaningful defence here on its own: the caller is a scheduler, not an
// attacker with a stopwatch, and a high-entropy secret is what actually
// protects the route. It is done anyway because it costs nothing and because
// leaving it out would invite the question again on every review.
//
// Every failure is one response. A missing header, an empty header, a wrong
// value, and a deployment that configured no secret at all are answered
// identically, in status, code and message. Distinguishing them would let an
// unauthenticated caller learn whether a deployment runs the cleanup, which is
// not something this route should report.
//
// The underlying error is nil on purpose. The shared error handler logs
// zap.Error(err), so wrapping the compared value in an error would write the
// secret into the log stream. Nothing else in this process logs request headers,
// so the value is never recorded anywhere.
func cleanupGuard(expected string) fiber.Handler {
	return func(c fiber.Ctx) error {
		presented := c.Get(cleanupSecretHeader)

		// The expected value is checked for emptiness before the comparison rather
		// than relying on an empty header failing the comparison by accident, so
		// an unconfigured deployment is closed by construction.
		//
		// The comparison is skipped entirely when it is unset, which also means an
		// unset secret never reaches a comparison at all rather than comparing
		// against empty bytes.
		if expected != "" &&
			subtle.ConstantTimeCompare(
				[]byte(presented),
				[]byte(expected),
			) == 1 {
			return c.Next()
		}

		// One construction point for the refusal, so "every failure is answered
		// identically" is true by construction rather than by two literals that
		// have to be kept in step.
		return apperror.UnauthorizedWith(
			CodeCleanupSecretInvalid,
			"missing or invalid cleanup secret",
			nil,
		)
	}
}

// MaintenanceRoutes registers the internal maintenance endpoints.
//
// This is a separate function, mounted on its own router, for the same reason
// collection.CollectionRoutes is: these are not authentication routes. Every
// /auth path either carries a Bearer token or takes its credential in the body,
// and an operator-triggered maintenance call holds neither. Registering this
// under /auth would put an unauthenticated route in the namespace where user
// credentials live and make that namespace stop meaning what it means.
//
// There is no rate limiter on these routes, deliberately. The existing budgets
// fail closed on an unreachable Redis, which is the right default for a route a
// client calls repeatedly, and exactly the wrong default for the one route an
// operator wants to be able to run while something else is broken. The guard
// refuses before any database access, so the work it protects is never started
// by a caller who has not presented the secret.
func MaintenanceRoutes(
	router fiber.Router,
	handler *Handler,
	cleanupSecret string,
) {
	router.Post(
		"/sessions/cleanup",
		cleanupGuard(cleanupSecret),
		handler.CleanupSessions,
	)
}
