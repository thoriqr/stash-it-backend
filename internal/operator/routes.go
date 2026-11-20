package operator

import "github.com/gofiber/fiber/v3"

// favicon is an empty icon.
//
// Browsers request /favicon.ico on their own, for every origin they load. Without a route
// the page answers 404 and the module logs a warning for something nobody asked for, which
// makes a real failure in that log harder to spot. Serving nothing is cheaper than serving
// an icon: the response is cached and the warning disappears.
var favicon = []byte{}

// Routes registers the operator page.
//
// The paths are the page's own, not the API's: "/" is the list, and the two mutations are
// form posts that redirect back to it. There is no JSON here, no envelope and no version
// prefix, because the consumer is a browser rendering HTML rather than a client parsing a
// contract.
func Routes(app *fiber.App, handler *Handler) {
	app.Get("/favicon.ico", func(c fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, "image/x-icon")
		c.Set(fiber.HeaderCacheControl, "public, max-age=86400")

		return c.Send(favicon)
	})

	app.Use(handler.Guard)

	app.Get("/", handler.Index)

	// HTML forms support GET and POST only, so a status change and a delete are both
	// POSTs. A method override would add a layer without adding anything: nothing on this
	// page would use PUT or DELETE, and the browser could not send them anyway.
	app.Post("/feedback/:id/status", handler.SetStatus)
	app.Post("/feedback/:id/delete", handler.Delete)
}
