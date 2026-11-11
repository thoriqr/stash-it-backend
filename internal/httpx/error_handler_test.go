package httpx_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
)

type fakeErrorReporter struct {
	mutex    sync.Mutex
	captured []*apperror.AppError
	eventID  string
}

func (r *fakeErrorReporter) CaptureHTTP(
	_ fiber.Ctx,
	appErr *apperror.AppError,
) string {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.captured = append(r.captured, appErr)

	return r.eventID
}

func (r *fakeErrorReporter) count() int {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return len(r.captured)
}

func (r *fakeErrorReporter) last() *apperror.AppError {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if len(r.captured) == 0 {
		return nil
	}

	return r.captured[len(r.captured)-1]
}

// Retry-After is the one header this handler writes, and it is written only when
// the error it was handed was built carrying a wait.
//
// The handler is the single place a response is written, so what it does or does
// not put on the wire is the whole of the behaviour. These tests drive it
// through a real request rather than calling it, because the question being
// asked is what a client receives.

// testApp mounts one route that fails with whatever error the test hands it.
func testApp(t *testing.T, appErr error) *fiber.App {
	t.Helper()

	return testAppWithReporter(t, appErr, nil)
}

func testAppWithReporter(
	t *testing.T,
	appErr error,
	reporter httpx.ErrorReporter,
) *fiber.App {
	t.Helper()

	app := fiber.New(fiber.Config{
		// The handler logs every failure it writes, and what is under test here is
		// the response rather than the log line, so the records go nowhere.
		ErrorHandler: httpx.NewErrorHandlerWithReporter(
			zap.NewNop(),
			reporter,
		),
	})

	app.Get("/fail", func(_ fiber.Ctx) error {
		return appErr
	})

	return app
}

func doRequest(t *testing.T, app *fiber.App) *http.Response {
	t.Helper()

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/fail", nil))
	require.NoError(t, err)

	return resp
}

type errorEnvelope struct {
	Error struct {
		Code    string                `json:"code"`
		Message string                `json:"message"`
		Fields  []apperror.ErrorField `json:"fields"`
	} `json:"error"`
}

// A 503 that was given a recovery time emits it.
//
// This is the response a server out of capacity needs: it is refusing work it
// could otherwise do, and the one useful thing it can say is roughly when a slot
// frees up. Without the header the client has nothing better to do than retry
// immediately, which is the exact request that would be refused again.
func TestErrorHandler_AServiceUnavailableWithAWaitEmitsRetryAfter(t *testing.T) {
	app := testApp(t, apperror.ServiceUnavailableWithRetryAfter(
		"A_CAPACITY_UNAVAILABLE",
		"the server is busy, please try again shortly",
		5*time.Second,
		errors.New("context deadline exceeded"),
	))

	resp := doRequest(t, app)

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, "5", resp.Header.Get(fiber.HeaderRetryAfter))

	body := decodeError(t, resp)
	require.Equal(t, "A_CAPACITY_UNAVAILABLE", body.Error.Code)
	require.Equal(
		t,
		"the server is busy, please try again shortly",
		body.Error.Message,
	)
}

// A 503 that was not given a recovery time emits nothing.
//
// This is the Redis outage case and the shape every unrelated fault takes. There
// is no moment to name — the counter may come back in a second or never — so a
// header here would be telling a client to come back at a time nobody chose,
// which is worse than saying nothing at all.
func TestErrorHandler_AServiceUnavailableWithoutAWaitEmitsNoHeader(t *testing.T) {
	cases := map[string]*apperror.AppError{
		"the generic form": apperror.ServiceUnavailable(
			errors.New("connection refused"),
		),
		// The exact form a Redis outage uses today, which is what must keep
		// behaving exactly as it did before any of this existed.
		"a rate limiter that cannot answer": apperror.ServiceUnavailableWith(
			"RATE_LIMIT_UNAVAILABLE",
			"request limiting is unavailable, please try again shortly",
			errors.New("dial tcp 127.0.0.1:6379: connect: connection refused"),
		),
	}

	for name, appErr := range cases {
		t.Run(name, func(t *testing.T) {
			resp := doRequest(t, testApp(t, appErr))

			require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
			require.Empty(
				t,
				resp.Header.Get(fiber.HeaderRetryAfter),
				"a 503 with no wait must not put a fabricated one on the wire",
			)
		})
	}
}

// The 429 behaviour is untouched.
//
// A rate-limited caller is still told exactly when its own window reopens, which
// is what the header was written for originally. This is the regression guard
// for widening what may carry the value: the common case must not have changed
// on the way past.
func TestErrorHandler_ARateLimitStillEmitsItsRetryAfter(t *testing.T) {
	resp := doRequest(t, testApp(t, apperror.TooManyRequestsWithRetryAfter(
		"RATE_LIMIT_EXCEEDED",
		"too many requests, please try again later",
		137*time.Second,
	)))

	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.Equal(t, "137", resp.Header.Get(fiber.HeaderRetryAfter))

	body := decodeError(t, resp)
	require.Equal(t, "RATE_LIMIT_EXCEEDED", body.Error.Code)
}

// A 429 with no wait configured emits nothing either, so the header is a
// function of the value being present rather than of the status.
func TestErrorHandler_ARateLimitWithoutAWaitEmitsNoHeader(t *testing.T) {
	resp := doRequest(t, testApp(t, apperror.TooManyRequests(
		errors.New("over budget"),
	)))

	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.Empty(t, resp.Header.Get(fiber.HeaderRetryAfter))
}

// An internal fault carries no header under any circumstance. It is the case that
// must be least tempted to fabricate one, because the server has no idea when it
// will recover.
func TestErrorHandler_AnInternalFaultEmitsNoHeader(t *testing.T) {
	resp := doRequest(t, testApp(t, apperror.Internal(
		errors.New("stored hash is unparseable"),
	)))

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Empty(t, resp.Header.Get(fiber.HeaderRetryAfter))
}

// A plain error that was never an AppError becomes a 500 rather than a panic or
// a bare status, and carries no header. This is the path every unhandled error
// in the application takes.
func TestErrorHandler_APlainErrorBecomesAnInternalFault(t *testing.T) {
	resp := doRequest(t, testApp(t, errors.New("something went wrong")))

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Empty(t, resp.Header.Get(fiber.HeaderRetryAfter))
}

// A wait rounds up on the wire rather than down, because the header means "not
// before". The number here is the one a client would actually read.
func TestErrorHandler_APartialSecondWaitIsRoundedUpOnTheWire(t *testing.T) {
	resp := doRequest(t, testApp(t, apperror.ServiceUnavailableWithRetryAfter(
		"A_CAPACITY_UNAVAILABLE",
		"busy",
		1500*time.Millisecond,
		nil,
	)))

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, "2", resp.Header.Get(fiber.HeaderRetryAfter))
}

func decodeError(t *testing.T, resp *http.Response) errorEnvelope {
	t.Helper()

	var body errorEnvelope

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	return body
}

// An unexpected server fault is reported. This is the only HTTP category the
// reporter receives, and the event id it returns is what the log line carries.
func TestErrorHandler_ReportsAnInternalFault(t *testing.T) {
	reporter := &fakeErrorReporter{eventID: "event-123"}

	resp := doRequest(t, testAppWithReporter(
		t,
		apperror.Internal(errors.New("database is unavailable")),
		reporter,
	))

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Equal(t, 1, reporter.count())
	require.Equal(t, apperror.CodeInternal, reporter.last().Code)
}

// A plain error that was never an AppError becomes a 500 and is reported for the
// same reason: it is a fault nothing in the application named.
func TestErrorHandler_ReportsAPlainErrorAsAnInternalFault(t *testing.T) {
	reporter := &fakeErrorReporter{}

	resp := doRequest(t, testAppWithReporter(
		t,
		errors.New("something went wrong"),
		reporter,
	))

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Equal(t, 1, reporter.count())
}

// Everything the API is supposed to answer with stays out of the reporter. A
// Redis outage is a 503, and a burst of bad input is a 4xx; neither is a defect,
// and sending them would bury real faults under expected traffic.
func TestErrorHandler_DoesNotReportExpectedOutcomes(t *testing.T) {
	cases := map[string]*apperror.AppError{
		"bad request":  apperror.BadRequest(errors.New("bad")),
		"validation":   apperror.Validation(nil, errors.New("bad")),
		"unauthorized": apperror.Unauthorized(nil),
		"forbidden":    apperror.Forbidden(nil),
		"not found":    apperror.NotFound(nil),
		"conflict":     apperror.Conflict(nil),
		"rate limit":   apperror.TooManyRequests(nil),
		"service unavailable": apperror.ServiceUnavailable(
			errors.New("redis down"),
		),
		"service unavailable with wait": apperror.ServiceUnavailableWithRetryAfter(
			"CAPACITY_UNAVAILABLE",
			"busy",
			time.Second,
			nil,
		),
	}

	for name, appErr := range cases {
		t.Run(name, func(t *testing.T) {
			reporter := &fakeErrorReporter{}

			resp := doRequest(t, testAppWithReporter(
				t,
				appErr,
				reporter,
			))

			require.Equal(t, appErr.Status, resp.StatusCode)
			require.Zero(
				t,
				reporter.count(),
				"an expected outcome must not reach the reporter",
			)
		})
	}
}
