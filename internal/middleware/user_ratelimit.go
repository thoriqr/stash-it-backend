package middleware

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

const (
	// CodeUserRateLimitExceeded reports that this authenticated user's budget for
	// the route is spent.
	//
	// It names the dimension rather than the endpoint, for the same reason
	// CodeIPRateLimitExceeded does: the path already says which route refused, and
	// a code per endpoint would be one more thing to keep in step. The route that
	// applies it is named by the namespace, which is chosen by the feature.
	CodeUserRateLimitExceeded = "USER_RATE_LIMIT_EXCEEDED"

	// CodeUserRateLimitUnavailable reports that the budget could not be evaluated.
	//
	// It is distinct from CodeUserRateLimitExceeded because the two ask the
	// caller to do opposite things: one means they asked for too much and should
	// wait, the other means the server could not tell and the request was never on
	// trial. Reporting the first for the second would tell a caller to wait out a
	// window nobody spent.
	CodeUserRateLimitUnavailable = "USER_RATE_LIMIT_UNAVAILABLE"
)

// errClaimsMissing is what a request reaching here without verified claims is
// reported as.
//
// It is a fault rather than a refusal on purpose. This middleware must be mounted
// after Auth, which refuses any request without a valid token and only then puts
// claims in the request, so the state is unreachable through any route that is
// wired the way this one is meant to be. Reaching it is a wiring mistake — the
// limiter is in front of a handler that expects an identity and there is none —
// and inventing a subject would merge every such request into one counter and
// hand a caller the choice of which budget it lands in.
var errClaimsMissing = errors.New("rate limited route reached without verified claims")

// UserRateLimit counts every request reaching the routes it is mounted on against
// one budget per authenticated user.
//
// It exists because an address is the wrong key for a route that acts on a user's
// own rows. The work behind these routes is per account — the saved items, the
// outbound fetch that reads them — and a caller reaches them with whatever
// network path it likes. An address-keyed budget does not survive an attacker
// changing address, and it charges a shared address for the activity of everyone
// behind it. The identity is already verified by Auth and is the thing ownership
// is checked against on every query these routes run, so it is the subject that
// matches the resource being protected.
//
// It must be mounted **after** Auth and before the handler. After, because the
// subject is the verified identity and there is nothing to key on otherwise;
// before, because a request this refuses must never reach the work it guards. A
// rejected request performs no fetch, writes nothing and enqueues nothing, so
// there is no partial state to reconcile.
//
// It fails closed. A limiter that cannot answer is not permission, and letting an
// unreachable counter through would let anyone remove the ceiling by causing the
// failure that produces it.
func UserRateLimit(
	limiter RateLimiter,
	namespace string,
	policy ratelimit.Policy,
) fiber.Handler {
	return func(c fiber.Ctx) error {
		claims, ok := c.Locals(AuthClaimsKey).(security.AccessTokenClaims)
		if !ok || claims.Subject == "" {
			return apperror.Internal(errClaimsMissing)
		}

		result, err := limiter.Allow(
			c.Context(),
			namespace,
			claims.Subject,
			policy,
		)
		if err != nil {
			return apperror.ServiceUnavailableWith(
				CodeUserRateLimitUnavailable,
				"request limiting is unavailable, please try again shortly",
				err,
			)
		}

		if !result.Allowed {
			return apperror.TooManyRequestsWithRetryAfter(
				CodeUserRateLimitExceeded,
				"too many requests, please try again later",
				result.RetryAfter,
			)
		}

		return c.Next()
	}
}
