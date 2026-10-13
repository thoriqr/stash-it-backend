package saved_item

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
	userRateLimiter middleware.RateLimiter,
) {
	// Saving is bounded per authenticated user, and mounted between Auth and the
	// handler for the same reason every other limiter in the application is mounted
	// before its handler: a refused request must not reach the work it guards. Here
	// that means no saved item is created and no enrichment task is enqueued.
	//
	// It runs after Auth because the budget belongs to the account, and an address
	// is not the thing being protected.
	createUserLimit := middleware.UserRateLimit(
		userRateLimiter,
		createUserRateLimitNamespace,
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

	// Reads and deletes are deliberately not limited. Neither performs an outbound
	// fetch, enqueues anything, or writes enrichment state; they are the same
	// database cost as every other authenticated read in the application, and a
	// limit here would bound no shared resource.
	router.Get(
		"/:id",
		middleware.Auth(verifier),
		handler.Get,
	)

	router.Delete(
		"/:id",
		middleware.Auth(verifier),
		handler.Delete,
	)
}
