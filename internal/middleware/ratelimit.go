package middleware

import (
	"context"

	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

const (
	// CodeIPRateLimitExceeded reports that this client's shared budget is spent.
	//
	// It is deliberately not named after the endpoint that applies it. The path
	// already says which endpoint refused, and a code that named the endpoint
	// would mean adding one code per endpoint and one more thing to keep in step.
	CodeIPRateLimitExceeded = "IP_RATE_LIMIT_EXCEEDED"

	// CodeIPRateLimitUnavailable reports that the budget could not be evaluated.
	//
	// It is distinct from CodeIPRateLimitExceeded because the two ask the caller
	// to do opposite things: one means the caller asked for too much and should
	// wait, the other means the server could not tell and the caller's request was
	// never on trial. Reporting the first for the second would tell a caller to
	// wait out a window nobody spent.
	CodeIPRateLimitUnavailable = "IP_RATE_LIMIT_UNAVAILABLE"

	// CodeClientIPUnavailable reports that the request's peer could not be
	// resolved to an address at all.
	CodeClientIPUnavailable = "CLIENT_IP_UNAVAILABLE"
)

// RateLimiter is the budget this middleware spends.
//
// It is declared here rather than imported from a feature because the middleware
// is the consumer, and a consuming package names its own dependencies. It is the
// same shape every limiter has, so wiring the Redis-backed implementation is all
// it takes to put this in front of any route.
type RateLimiter interface {
	Allow(
		ctx context.Context,
		namespace string,
		subject string,
		policy ratelimit.Policy,
	) (ratelimit.Result, error)
}

// IPRateLimit counts every request reaching the routes it is mounted on against
// one budget shared by whatever address the request came from.
//
// It runs as middleware rather than in a service, and that placement is the whole
// reason it exists. A service never sees a request that failed to parse its path
// parameter, or that named a verification that does not exist, so a limit enforced
// there would count only the requests that were already well-formed. An attacker
// probing for valid ids, or simply flooding, would pay nothing for the guessing.
// Mounting in front of the handler is what makes the count cover the attempts
// rather than only the successes.
//
// Mounted on two routes, it enforces one budget across both. The budget belongs to
// the address, not to the path, so switching between two endpoints that send mail
// to the same place cannot double the allowance.
//
// A request this refuses never reaches the handler, so it also never reaches a
// later limiter in the same request. That ordering is what stops a caller from
// spending a second layer's budget on requests the first layer already turned
// away.
//
// It fails closed. A limiter that cannot answer is not permission, and letting an
// unreachable counter through would let anyone remove the ceiling by causing a
// failure. The same reasoning applies to an address that cannot be resolved: an
// unresolved request shares one subject with every other unresolved request, and
// refusing is honest where inventing a subject would silently block unrelated
// callers.
func IPRateLimit(
	limiter RateLimiter,
	namespace string,
	policy ratelimit.Policy,
) fiber.Handler {
	return func(c fiber.Ctx) error {
		clientIP, ok := ratelimit.NormalizeIP(c.IP())
		if !ok {
			return apperror.ServiceUnavailableWith(
				CodeClientIPUnavailable,
				"unable to determine the client address",
				nil,
			)
		}

		result, err := limiter.Allow(
			c.Context(),
			namespace,
			clientIP,
			policy,
		)
		if err != nil {
			return apperror.ServiceUnavailableWith(
				CodeIPRateLimitUnavailable,
				"request limiting is unavailable, please try again shortly",
				err,
			)
		}

		if !result.Allowed {
			return apperror.TooManyRequestsWithRetryAfter(
				CodeIPRateLimitExceeded,
				"too many requests, please try again later",
				result.RetryAfter,
			)
		}

		return c.Next()
	}
}
