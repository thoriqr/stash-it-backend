package health

import "github.com/gofiber/fiber/v3"

// Routes registers the liveness probe.
//
// /livez rather than /health, so that neither the route name nor a response
// from it can be mistaken for a statement about the application's dependencies.
// The distinction is recorded on Handler.Check, which is where the reasoning
// belongs; this is only the path.
//
// It is mounted on the root app rather than a group, like the other
// infrastructure routes. It is deliberately not given middleware.Auth: a health
// probe is called by the platform, which holds no user credential, so requiring
// one would make the endpoint unreachable for the only caller that matters.
func Routes(app *fiber.App, h *Handler) {
	app.Get("/livez", h.Check)
}