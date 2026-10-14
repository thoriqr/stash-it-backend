package health

import "github.com/gofiber/fiber/v3"

// Handler answers the liveness probe.
//
// It has no fields on purpose. There is nothing to inject, because the one
// property this endpoint has is that it answers without consulting anything.
type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

// Check reports that this process is running and able to serve HTTP.
//
// It means exactly that and nothing more. **It is not a statement about the
// application or about any dependency.** A 200 here is compatible with an
// unreachable database, an unreachable Redis, and every request in between
// returning 500 or 503.
//
// That is deliberate, and it is the reason the name is `/livez`. A liveness
// probe answers "is this process wedged, and would restarting it help?" and the
// answer to that must stay yes during an outage whose cause is external.
//
// **Do not add a dependency check here.** This application's rate limiters fail
// closed by choice — an unreachable Redis stops PIN issuance, manual login,
// Google authentication and token refresh outright (see PROGRESS.md, "Outstanding
// deployment work", item 3). If this endpoint also reported on Redis or
// PostgreSQL, that same outage would fail the probe, and a platform acting on a
// failing health check would restart or drain instances at exactly the moment the
// system is already degraded. That converts a service that is degraded but
// serving into one that is entirely unavailable, and restarting a process does
// not bring an external dependency back. The outage would be made worse by the
// very check meant to detect it.
//
// Startup already covers the other half of the question. database.NewPool pings
// PostgreSQL and both limiter Check calls verify Redis, and main returns rather
// than serving if any of them fails — and the listener does not open until every
// module is registered. An instance that answers this request at all has
// therefore already passed every startup check, so "responds" and "fully booted"
// are the same statement here.
//
// The response is a flat object rather than the httpx {data, message} envelope
// the rest of the API uses. A platform probe branches on the status code and
// rarely parses the body, so the envelope would add a key with no consumer.
//
// It is unauthenticated, and must stay so: a platform calls it without holding
// any credential.
func (h *Handler) Check(c fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status": "ok",
	})
}