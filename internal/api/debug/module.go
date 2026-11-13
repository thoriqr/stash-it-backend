package debug

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/config"
)

// RegisterModule mounts the deliberate-failure routes when the deployment asked
// for them.
//
// SENTRY_DEBUG is the switch. It is the existing configuration value for
// diagnosing the reporter, so verifying reporting does not need a new variable, a
// new config type or a second way to say the same thing.
//
// When it is false — the value production runs with — this returns without
// registering anything, and the routes do not exist at all.
func RegisterModule(app *fiber.App, cfg config.Config) {
	if !cfg.Sentry.Debug {
		return
	}

	Routes(app, NewHandler())
}
