package apperror_test

import (
	"errors"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// What a wait on an error becomes.
//
// The header's contract is "not before", so rounding down would name a moment
// the window is still closed and a client that trusted it would come back early
// and be refused for nothing. The zero case exists because a caller that has
// nothing useful to say must not put something impossible on the wire.
func TestRetryAfterSeconds_RoundsUpAndNeverGoesNegative(t *testing.T) {
	cases := map[string]struct {
		wait time.Duration
		want int
	}{
		"a whole number of seconds is unchanged": {
			wait: 137 * time.Second,
			want: 137,
		},
		"a partial second rounds up to one": {
			wait: time.Millisecond,
			want: 1,
		},
		// 59.001s must not be reported as 59s: that is 1ms before the window
		// closes, and a client arriving then is refused again.
		"just over a second rounds up to two": {
			wait: 59_001 * time.Millisecond,
			want: 60,
		},
		"exactly on a second boundary does not round": {
			wait: 59 * time.Second,
			want: 59,
		},
		"zero is zero": {
			wait: 0,
			want: 0,
		},
		"a negative wait is zero, never negative": {
			wait: -30 * time.Second,
			want: 0,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			require.NotNil(t, apperror.RetryAfterSeconds(tc.wait))
			require.Equal(t, tc.want, *apperror.RetryAfterSeconds(tc.wait))
		})
	}
}

// A 503 whose recovery time is known carries it, and carries nothing else that
// the plain 503 does not already carry. A future caller refusing work because it
// is temporarily out of capacity builds its refusal here; nothing about it should
// look different from any other 503 apart from the wait.
func TestServiceUnavailableWithRetryAfter_CarriesTheWaitWithoutChangingTheError(t *testing.T) {
	underlying := errors.New("context deadline exceeded")

	appErr := apperror.ServiceUnavailableWithRetryAfter(
		"A_CAPACITY_UNAVAILABLE",
		"the server is busy, please try again shortly",
		3*time.Second,
		underlying,
	)

	require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
	require.Equal(t, "A_CAPACITY_UNAVAILABLE", appErr.Code)
	require.Equal(
		t,
		"the server is busy, please try again shortly",
		appErr.Message,
	)

	require.NotNil(t, appErr.RetryAfterSeconds())
	require.Equal(t, 3, *appErr.RetryAfterSeconds())

	// The cause is worth logging, and losing it would leave the refusal with
	// nothing to explain why it happened.
	require.ErrorIs(t, appErr, underlying)
	require.Equal(t, []apperror.ErrorField{}, appErr.Fields)
}

// The plain 503 constructors carry no wait, and that is the property the header
// emission below depends on.
//
// A 503 from a limiter that cannot reach Redis, or from any other fault with no
// recovery time anyone could name, must reach the wire bare. If either of these
// grew a wait, every such response would start telling a client to come back at
// a moment nobody chose.
func TestServiceUnavailable_CarriesNoWait(t *testing.T) {
	generic := apperror.ServiceUnavailable(errors.New("connection refused"))
	require.Nil(
		t,
		generic.RetryAfterSeconds(),
		"a fault with no recovery time must not invent one",
	)

	custom := apperror.ServiceUnavailableWith(
		"RATE_LIMIT_UNAVAILABLE",
		"try again shortly",
		errors.New("connection refused"),
	)
	require.Nil(
		t,
		custom.RetryAfterSeconds(),
		"the form a Redis outage uses must not invent a wait either",
	)
}

// An empty code or message falls back rather than producing an error with no
// code, matching every other constructor in the package. A caller that forgot to
// name the refusal still gets something the response envelope can carry.
func TestServiceUnavailableWithRetryAfter_EmptyCodeAndMessageFallBack(t *testing.T) {
	appErr := apperror.ServiceUnavailableWithRetryAfter(
		"",
		"",
		time.Second,
		nil,
	)

	require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
	require.Equal(t, apperror.CodeUnavailable, appErr.Code)
	require.Equal(t, apperror.MessageUnavailable, appErr.Message)
	require.NotNil(t, appErr.RetryAfterSeconds())
	require.Equal(t, 1, *appErr.RetryAfterSeconds())
}

// The 429 twin is unchanged by any of this. A rate-limited caller keeps being
// told exactly when its own window reopens, which is the behaviour the header
// existed for in the first place.
func TestTooManyRequestsWithRetryAfter_IsUnchanged(t *testing.T) {
	appErr := apperror.TooManyRequestsWithRetryAfter(
		"RATE_LIMIT_EXCEEDED",
		"too many requests, please try again later",
		90*time.Second,
	)

	require.Equal(t, http.StatusTooManyRequests, appErr.Status)
	require.Equal(t, "RATE_LIMIT_EXCEEDED", appErr.Code)

	require.NotNil(t, appErr.RetryAfterSeconds())
	require.Equal(t, 90, *appErr.RetryAfterSeconds())
}

// What an error reporter should receive.
//
// The boundary is the HTTP status rather than the constructor: an unexpected
// 5xx is a defect, a 4xx is an outcome the API exists to produce, and a 503 is
// known operational unavailability. Sending the last two would make a Redis
// outage or a burst of bad input look like a bug.
func TestAppError_ReportableIsLimitedToUnexpectedServerFaults(t *testing.T) {
	underlying := errors.New("database is unavailable")

	cases := map[string]struct {
		appErr *apperror.AppError
		want   bool
	}{
		"internal fault": {
			appErr: apperror.Internal(underlying),
			want:   true,
		},
		"custom internal fault": {
			appErr: apperror.InternalWith(
				"CUSTOM_INTERNAL",
				"internal failure",
				underlying,
			),
			want: true,
		},
		"plain error converted to internal": {
			appErr: apperror.FromError(underlying),
			want:   true,
		},
		"service unavailable": {
			appErr: apperror.ServiceUnavailable(underlying),
			want:   false,
		},
		"service unavailable with wait": {
			appErr: apperror.ServiceUnavailableWithRetryAfter(
				"CAPACITY_UNAVAILABLE",
				"busy",
				time.Second,
				underlying,
			),
			want: false,
		},
		"bad request": {
			appErr: apperror.BadRequest(underlying),
			want:   false,
		},
		"validation": {
			appErr: apperror.Validation(nil, underlying),
			want:   false,
		},
		"unauthorized": {
			appErr: apperror.Unauthorized(underlying),
			want:   false,
		},
		"forbidden": {
			appErr: apperror.Forbidden(underlying),
			want:   false,
		},
		"not found": {
			appErr: apperror.NotFound(underlying),
			want:   false,
		},
		"conflict": {
			appErr: apperror.Conflict(underlying),
			want:   false,
		},
		"too many requests": {
			appErr: apperror.TooManyRequests(underlying),
			want:   false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.appErr.Reportable())
		})
	}
}

//go:noinline
func internalErrorFromHelper() *apperror.AppError {
	return apperror.Internal(errors.New("database is unavailable"))
}

//go:noinline
func internalWithErrorFromHelper() *apperror.AppError {
	return apperror.InternalWith(
		"CUSTOM_INTERNAL",
		"internal failure",
		errors.New("database is unavailable"),
	)
}

// The stack an internal error carries starts at the caller of the constructor,
// not at the constructor itself and not at the HTTP handler that later reports
// it. This is what lets Sentry point at the repository or service line that
// failed.
func TestInternal_CapturesTheCallerStack(t *testing.T) {
	pcs := internalErrorFromHelper().StackTrace()

	require.NotEmpty(t, pcs)

	frames := runtime.CallersFrames(pcs)
	first, _ := frames.Next()

	require.Contains(t, first.Function, "internalErrorFromHelper")
	require.NotContains(t, first.Function, "apperror.Internal")
}

func TestInternalWith_CapturesTheCallerStack(t *testing.T) {
	pcs := internalWithErrorFromHelper().StackTrace()

	require.NotEmpty(t, pcs)

	frames := runtime.CallersFrames(pcs)
	first, _ := frames.Next()

	require.Contains(t, first.Function, "internalWithErrorFromHelper")
	require.NotContains(t, first.Function, "apperror.InternalWith")
}

// Capturing a stack is reserved for unexpected faults. A 4xx or a 503 is an
// outcome the API is supposed to produce, and paying for a stack there would be
// wasted work on every rejected request.
func TestAppError_ExpectedOutcomesCarryNoStack(t *testing.T) {
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
			require.Empty(t, appErr.StackTrace())
		})
	}
}
