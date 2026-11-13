package debug

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// Handler serves the two deliberate-failure routes.
//
// They exist because a 5xx is close to unreachable on demand in this application:
// every failure the API can produce from bad input is a 4xx, and every dependency
// outage is mapped to 503, which is deliberately not reported. That is healthy
// behaviour, and it also means "does the error reporter still work in production"
// cannot be answered by waiting for an incident.
//
// A nil Handler is never registered: Routes is only called when the deployment
// asked for these routes, so their existence is a deployment decision rather than
// a runtime check inside a handler.
type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

// Fail returns a 500 through the normal error path.
//
// It produces an apperror value rather than writing a response, so the request
// travels the same route every real server fault does: the app's error handler,
// apperror.Reportable, and CaptureHTTP. That is what makes a success here evidence
// about the reporter rather than about this handler.
func (h *Handler) Fail(c fiber.Ctx) error {
	return apperror.InternalWith(
		"sentry_debug_error",
		"deliberate 500 produced to verify error reporting",
		errors.New("debug: deliberate internal error"),
	)
}

// Panic panics on purpose.
//
// It is the second path on purpose. Panics are captured by Fiber's recoverer
// before the error handler sees them, through a different call than a 500, so a
// deployment can have one working and the other broken. Verifying only the 500
// would leave that untested.
func (h *Handler) Panic(_ fiber.Ctx) error {
	panic("debug: deliberate panic")
}
