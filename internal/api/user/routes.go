package user

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
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
func Routes(
	router fiber.Router,
	h *Handler,
	verifier *security.AccessTokenVerifier,
) {
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
		h.SetPassword,
	)
}
