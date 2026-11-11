package observability_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/observability"
)

// An empty DSN is the ordinary local-development state: the reporter exists so
// call sites do not need nil checks, but it sends nothing and returns no event
// id. The logger remains the only sink.
func TestInit_EmptyDSNDisablesReporting(t *testing.T) {
	reporter, err := observability.Init(observability.Config{}, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, reporter)
	require.False(t, reporter.Enabled())

	eventID := reporter.CaptureHTTP(
		nil,
		apperror.Internal(errors.New("database is unavailable")),
	)

	require.Empty(t, eventID)

	reporter.Flush(time.Second)
}

func TestDisabled_IsDisabled(t *testing.T) {
	reporter := observability.Disabled(zap.NewNop())

	require.NotNil(t, reporter)
	require.False(t, reporter.Enabled())
}

// A malformed DSN is reported rather than silently leaving reporting off. The
// caller decides whether that is fatal; the API logs it and keeps running.
func TestInit_RejectsAMalformedDSN(t *testing.T) {
	_, err := observability.Init(observability.Config{
		DSN: "not-a-dsn",
	}, zap.NewNop())

	require.Error(t, err)
}

// The captured stack must be the one Sentry uses. This is the integration point
// between apperror and the SDK: ExtractStacktrace recognizes the StackTrace
// method and returns those frames instead of the capture point.
func TestAppError_ProvidesSentryWithTheCapturedStack(t *testing.T) {
	appErr := apperror.Internal(errors.New("database is unavailable"))

	stack := sentry.ExtractStacktrace(appErr)

	require.NotNil(t, stack)
	require.NotEmpty(t, stack.Frames)
}

// A panic is still logged when reporting is disabled. This is the path a local
// development build takes, and the stack must not disappear just because no DSN
// was configured.
func TestReporter_CapturePanicWithDisabledReportingStillCompletes(t *testing.T) {
	reporter := observability.Disabled(zap.NewNop())

	app := fiber.New()
	app.Get("/panic", func(c fiber.Ctx) error {
		reporter.CapturePanic(c, "boom")

		return c.SendStatus(http.StatusInternalServerError)
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/panic", nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}
