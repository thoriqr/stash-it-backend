package operator

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// errorHandlerApp mounts the handler under test with nothing else, so the only thing being
// exercised is how an error the framework produced reaches the client and the log.
func errorHandlerApp(t *testing.T, log *zap.Logger) *fiber.App {
	t.Helper()

	app := fiber.New(fiber.Config{ErrorHandler: newErrorHandler(log)})

	// One working route, so a request that matches something is distinguishable from one
	// that does not.
	app.Get("/", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})

	return app
}

// doErrorRequest issues one request and reports the status and the log lines produced.
func doErrorRequest(
	t *testing.T,
	app *fiber.App,
	method string,
	target string,
) int {
	t.Helper()

	resp, err := app.Test(httptest.NewRequest(method, target, nil))
	require.NoError(t, err)

	return resp.StatusCode
}

// A path nothing serves is a 404, not a 500. Chrome requests
// /.well-known/appspecific/com.chrome.devtools.json on every origin it loads, and a 500
// for that would tell the browser the page was broken.
func TestErrorHandler_UnknownPathIs404(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	app := errorHandlerApp(t, zap.New(core))

	status := doErrorRequest(
		t,
		app,
		http.MethodGet,
		"/.well-known/appspecific/com.chrome.devtools.json",
	)
	require.Equal(t, http.StatusNotFound, status)

	// Nothing is logged: a browser asking for something that was never here is not a
	// problem, and a line per request would bury the failures that are.
	require.Empty(t, logs.All(), "a 404 must not be logged on this listener")
}

// A method the route does not accept is a 405, for the same reason.
func TestErrorHandler_WrongMethodIs405(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	app := errorHandlerApp(t, zap.New(core))

	status := doErrorRequest(t, app, http.MethodPost, "/")
	require.Equal(t, http.StatusMethodNotAllowed, status)

	require.Empty(t, logs.All(), "a 405 must not be logged on this listener")
}

// An ordinary request is untouched: this handler only exists for errors.
func TestErrorHandler_WorkingRouteIsUnaffected(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	app := errorHandlerApp(t, zap.New(core))

	status := doErrorRequest(t, app, http.MethodGet, "/")
	require.Equal(t, http.StatusOK, status)

	require.Empty(t, logs.All())
}

// A genuine fault still reaches the log, at error level, with the status and the path.
//
// This is the half that must keep working: silencing browser noise is only safe because
// everything above 4xx is still reported.
func TestErrorHandler_ServerFaultIsLogged(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	app := errorHandlerApp(t, zap.New(core))

	app.Get("/boom", func(fiber.Ctx) error {
		return fiber.ErrInternalServerError
	})

	status := doErrorRequest(t, app, http.MethodGet, "/boom")
	require.Equal(t, http.StatusInternalServerError, status)

	entries := logs.All()
	require.Len(t, entries, 1)

	require.Equal(t, zapcore.ErrorLevel, entries[0].Level)

	fields := entries[0].ContextMap()
	require.Equal(t, "/boom", fields["path"])
	require.EqualValues(t, http.StatusInternalServerError, fields["status"])
}
