package feedback

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// Routes registers the user-facing surface.
//
// Filing a report is the only thing a user can do with feedback. There is no GET, no
// edit and no delete on this surface: a report is written once, and what happens to it
// afterwards is the operator's business. Offering a user the ability to read back
// their reports would promise a conversation this feature does not have.
//
// There are deliberately no operator routes here. Triage is not part of this API at
// all: it happens on a page that listens on loopback, which is documented in
// documentation/feedback.md and is not described by Swagger. A route guarded by a
// shared secret would still be a public route — reachable by anyone who found the
// secret, and reachable by anyone at all if it were forgotten — and the operator
// surface does not need one, because SSH is the authentication.
func Routes(
	router fiber.Router,
	handler *Handler,
	verifier *security.AccessTokenVerifier,
	userRateLimiter middleware.RateLimiter,
) {
	// Filing is bounded per authenticated user, mounted after Auth because the budget
	// belongs to the account, and before the handler because a refused request must
	// not reach the insert it guards.
	//
	// This is the tightest user budget in the application, and what it protects is the
	// operator's attention rather than a shared resource: the insert itself is cheap.
	// It bounds a flood, not noise. Removing what does arrive is the operator's job.
	createUserLimit := middleware.UserRateLimit(
		userRateLimiter,
		CreateUserRateLimitNamespace,
		ratelimit.Policy{
			Max:    CreateUserRateLimitMax,
			Window: CreateUserRateLimitWindow,
		},
	)

	router.Post(
		"/",
		middleware.Auth(verifier),
		createUserLimit,
		handler.Create,
	)
}
