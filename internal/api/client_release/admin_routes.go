package client_release

import (
	"crypto/subtle"

	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// releaseGuard is the authentication for the internal release maintenance routes.
//
// It follows the session cleanup guard: the caller is an operator tool, not a
// user, so no Bearer token applies. The secret is compared with
// subtle.ConstantTimeCompare, every failure is answered identically, and an
// unconfigured deployment is closed by construction because the comparison is
// skipped when no secret was supplied.
//
// Every failure is one response, in status, code and message. Distinguishing a
// missing header from a wrong value or from an unconfigured deployment would let
// an unauthenticated caller learn whether the maintenance surface is enabled.
func releaseGuard(expected string) fiber.Handler {
	return func(c fiber.Ctx) error {
		presented := c.Get(releaseSecretHeader)

		if expected != "" &&
			subtle.ConstantTimeCompare(
				[]byte(presented),
				[]byte(expected),
			) == 1 {
			return c.Next()
		}

		return apperror.UnauthorizedWith(
			CodeInvalidReleaseSecret,
			"missing or invalid release secret",
			nil,
		)
	}
}

// MaintenanceRoutes registers the internal client-release maintenance endpoints.
//
// It is mounted under /internal/maintenance, the same prefix as session cleanup,
// because these are operator routes rather than user routes. There is no rate
// limiter deliberately: the existing limiter fails closed when Redis is
// unreachable, and this is exactly the route an operator wants to be able to call
// while something else is broken. The guard refuses before any database access.
func MaintenanceRoutes(
	router fiber.Router,
	h *AdminHandler,
	secret string,
) {
	router.Post(
		"/client-releases",
		releaseGuard(secret),
		h.PublishRelease,
	)

	router.Post(
		"/client-releases/force",
		releaseGuard(secret),
		h.ForceRelease,
	)

	router.Post(
		"/client-releases/rollback",
		releaseGuard(secret),
		h.RollbackRelease,
	)

	router.Get(
		"/client-releases",
		releaseGuard(secret),
		h.ListReleases,
	)
}
