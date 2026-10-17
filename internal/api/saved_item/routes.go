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

	// The recent history. Registered on "/" rather than on a distinct sub-path,
	// because this is the listing of the resource this group is mounted on and it
	// belongs where a client looks for one. It does not collide with GET
	// /saved-items/{id} below: that is a static-plus-param path and this one has no
	// parameter, so the router never has to choose between them.
	//
	// Deliberately not rate limited, and deliberately mounted after Auth with nothing
	// between them. A listing performs one indexed read, opens no transaction, holds
	// no cursor or lock that outlives the request and spends no shared resource, so a
	// limit here would bound nothing while making the most-read screen in the app
	// fail closed whenever Redis is unavailable. The two routes below that do cost
	// something are limited: a save arranges unbounded downstream work, and an
	// on-demand enrichment performs an outbound fetch in this process.
	router.Get(
		"/",
		middleware.Auth(verifier),
		handler.List,
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
