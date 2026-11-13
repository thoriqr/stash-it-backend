package client_release

import "github.com/gofiber/fiber/v3"

// Routes registers the public client-release check.
//
// No middleware.Auth: the check must work before login, and a broken auth API is
// exactly when a client needs the update path. The response carries no user data.
func Routes(router fiber.Router, h *Handler) {
	router.Get("/latest", h.Check)
}
