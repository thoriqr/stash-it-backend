package user

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// Routes registers the user profile endpoints.
//
// The router is supplied by the module, which mounts this feature under the
// /users prefix. Every route here is about the caller rather than about a
// resource somebody else could name, so there is no :id anywhere in this file and
// no route that could be pointed at a second account.
//
// Auth is applied per route with middleware.Auth, never router.Use, for the
// reason it is everywhere else in this repository: each route states its own
// requirement, and adding a route to a group must never make it authenticated by
// accident or leave it unauthenticated by accident.
//
// Only the password route is budgeted. The two profile routes deliberately carry
// no limiter — see PasswordUserRateLimitMax for why, and for why the absence is
// worth more than a limit would be.
func Routes(
	router fiber.Router,
	h *Handler,
	verifier *security.AccessTokenVerifier,
	userRateLimiter middleware.RateLimiter,
) {
	// Bounded per authenticated user, and mounted between Auth and the handler for
	// the reason every other user budget in this application is mounted there: the
	// subject is the verified identity and there is nothing to key on before Auth
	// has run, and a refused request must never reach the derivations it guards. A
	// refusal writes no credential and revokes no session, so there is no partial
	// state to reconcile.
	//
	// It is keyed on the account rather than the address because the work is
	// proportional to password derivations against the process-wide pool this
	// account shares with everybody else. An address budget would not survive a
	// caller changing address, and it would charge a household or an office behind
	// one NAT for one person's password work.
	passwordUserLimit := middleware.UserRateLimit(
		userRateLimiter,
		passwordUserRateLimitNamespace,
		ratelimit.Policy{
			Max:    PasswordUserRateLimitMax,
			Window: PasswordUserRateLimitWindow,
		},
	)

	router.Get(
		"/me",
		middleware.Auth(verifier),
		h.GetUser,
	)

	router.Patch(
		"/me",
		middleware.Auth(verifier),
		h.UpdateUser,
	)

	router.Put(
		"/me/password",
		middleware.Auth(verifier),
		passwordUserLimit,
		h.SetPassword,
	)
}
