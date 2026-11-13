package debug

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
)

// newTestApp mirrors the composition that matters: the app's error handler and the
// recoverer, in the order main wires them.
func newTestApp() *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler: httpx.NewErrorHandler(zap.NewNop()),
	})

	app.Use(recoverer.New(recoverer.Config{
		PanicHandler: func(_ fiber.Ctx, _ any) error {
			return fiber.ErrInternalServerError
		},
	}))

	return app
}

// The switch is the whole point: with SENTRY_DEBUG off, the routes must not exist
// rather than merely refuse.
func TestRegisterModule_DoesNotRegisterWhenDebugIsOff(t *testing.T) {
	app := newTestApp()

	RegisterModule(app, config.Config{})

	for _, path := range []string{
		"/debug/sentry/error",
		"/debug/sentry/panic",
	} {
		response, err := app.Test(
			httptest.NewRequest(http.MethodPost, path, nil),
		)
		require.NoError(t, err)

		require.Equal(t, http.StatusNotFound, response.StatusCode)
	}
}

func TestRegisterModule_RegistersWhenDebugIsOn(t *testing.T) {
	app := newTestApp()

	RegisterModule(app, config.Config{
		Sentry: config.SentryConfig{Debug: true},
	})

	response, err := app.Test(
		httptest.NewRequest(http.MethodPost, "/debug/sentry/error", nil),
	)
	require.NoError(t, err)

	require.Equal(t, http.StatusInternalServerError, response.StatusCode)
}

// The 500 must arrive as an apperror that the reporter would accept, not as a
// response this handler wrote itself.
func TestFail_ReturnsAReportableError(t *testing.T) {
	var captured *apperror.AppError

	app := fiber.New(fiber.Config{
		ErrorHandler: func(c fiber.Ctx, err error) error {
			captured = apperror.FromError(err)

			return httpx.NewErrorHandler(zap.NewNop())(c, err)
		},
	})
	app.Post("/fail", NewHandler().Fail)

	response, err := app.Test(
		httptest.NewRequest(http.MethodPost, "/fail", nil),
	)
	require.NoError(t, err)

	require.Equal(t, http.StatusInternalServerError, response.StatusCode)

	require.NotNil(t, captured)
	require.True(
		t,
		captured.Reportable(),
		"the 500 must be reportable, or no reporter would ever see it",
	)
}

// The panic must be recoverable and must surface as a 500 rather than tearing the
// process down, which is what makes the route usable against a live deployment.
func TestPanic_IsRecoveredAsAServerError(t *testing.T) {
	app := newTestApp()

	app.Post("/panic", NewHandler().Panic)

	response, err := app.Test(
		httptest.NewRequest(http.MethodPost, "/panic", nil),
	)
	require.NoError(t, err)

	require.Equal(t, http.StatusInternalServerError, response.StatusCode)
}
