package debug

import (
	"github.com/gofiber/fiber/v3"
)

// Routes registers the deliberate-failure routes.
//
// Callers pass register only when the deployment asked for them. Doing the check
// here instead would leave the routes in the route table, and a route that exists
// and refuses is still a route somebody can probe and still appears in the
// startup handler count.
//
// They are not behind middleware.Auth on purpose. The caller that needs them is
// an operator with a shell and a browser, and requiring a user credential would
// mean creating an account to test the reporter. The gate is the configuration
// flag, the routes must be removed after use, and neither route reads or writes
// any data.
func Routes(app *fiber.App, h *Handler) {
	app.Post("/debug/sentry/error", h.Fail)
	app.Post("/debug/sentry/panic", h.Panic)
}
