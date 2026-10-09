package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	registration "github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	registrationtestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/registration/generated"
)

// These tests drive the endpoints through HTTP with a limiter the test controls,
// so the whole path is exercised: route, middleware, handler, service, limiter,
// and the message that would have been sent.
//
// A request crosses two budgets before a PIN exists: the client address, spent by
// middleware in front of the handler, and the email address, spent by the service
// once the verification has been resolved. Every test below states what both do,
// because a test that only arranged one would be asserting on whichever was
// consulted first.

type registerAPIError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

const (
	pinPath    = "/pin"
	resendPath = "/resend"
)

// openPendingRegistration creates a manual registration and returns its
// verification id, which is what both PIN endpoints are addressed by.
func openPendingRegistration(t *testing.T, email string) uuid.UUID {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/manual",
		strings.NewReader(`{"email":"`+email+`"}`),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var body struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotEqual(t, uuid.Nil, body.Data.VerificationID)

	return body.Data.VerificationID
}

// postPinEndpoint calls one of the two issuance endpoints and returns the
// response status, the error code if any, and the Retry-After header.
func postPinEndpoint(
	t *testing.T,
	app *fiber.App,
	verificationID uuid.UUID,
	path string,
) pinResponse {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/verification/"+verificationID.String()+path,
		nil,
	)

	resp, err := app.Test(req)
	require.NoError(t, err)

	result := pinResponse{
		status:     resp.StatusCode,
		retryAfter: resp.Header.Get(fiber.HeaderRetryAfter),
	}

	if result.status < 400 {
		return result
	}

	var body registerAPIError

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	result.code = body.Error.Code

	return result
}

type pinResponse struct {
	status     int
	code       string
	retryAfter string
}

// An exhausted email budget is a 429 with the feature's own code, and it costs
// nothing: no message went out.
//
// The client address budget is explicitly allowed through, so what is refused
// here is the email budget and not the one in front of it.
func TestRegisterAPI_PinRateLimit_ExceededReturns429(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const email = "ratelimit-exceeded@example.com"

	verificationID := openPendingRegistration(t, email)

	limiter := testutil.ExceededPinRateLimiter().
		AllowNamespace(testutil.PinIPNamespace, testutil.NamespaceOutcome{
			Allowed: true,
		})

	app, emailSender := testutil.NewAppWithLimiter(testPool, limiter)

	result := postPinEndpoint(t, app, verificationID, pinPath)
	require.Equal(t, http.StatusTooManyRequests, result.status)
	require.Equal(t, registration.CodePinRateLimitExceeded, result.code)

	result = postPinEndpoint(t, app, verificationID, resendPath)
	require.Equal(t, http.StatusTooManyRequests, result.status)
	require.Equal(t, registration.CodePinRateLimitExceeded, result.code)

	require.Empty(t, emailSender.Messages, "a refused request must send no message")
}

// The email budget reports how long is left in its window, as a header a client
// can act on rather than as something it has to guess at.
func TestRegisterAPI_PinRateLimit_ExceededCarriesRetryAfter(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	verificationID := openPendingRegistration(
		t,
		"ratelimit-retry-after@example.com",
	)

	limiter := testutil.ExceededPinRateLimiter().
		AllowNamespace(testutil.PinIPNamespace, testutil.NamespaceOutcome{
			Allowed: true,
		}).
		AllowNamespace(testutil.PinEmailNamespace, testutil.NamespaceOutcome{
			Allowed:    false,
			RetryAfter: 754 * time.Second,
		})

	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	result := postPinEndpoint(t, app, verificationID, pinPath)

	require.Equal(t, http.StatusTooManyRequests, result.status)
	require.Equal(t, "754", result.retryAfter)
}

// A limiter that cannot answer is a 503, not a 429 and not a success. The
// distinction matters: 429 says "you asked for too much", 503 says "we could not
// tell", and only one of those is the caller's fault.
//
// The whole limiter is failing here, so the client budget is the one that meets
// the request first and the code reported is that layer's.
func TestRegisterAPI_IPRateLimit_UnavailableReturns503(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	verificationID := openPendingRegistration(
		t,
		"ratelimit-unavailable@example.com",
	)

	limiter := testutil.UnavailablePinRateLimiter(
		errors.New("dial tcp: connection refused"),
	)

	app, emailSender := testutil.NewAppWithLimiter(testPool, limiter)

	result := postPinEndpoint(t, app, verificationID, pinPath)
	require.Equal(t, http.StatusServiceUnavailable, result.status)
	require.Equal(t, "IP_RATE_LIMIT_UNAVAILABLE", result.code)

	result = postPinEndpoint(t, app, verificationID, resendPath)
	require.Equal(t, http.StatusServiceUnavailable, result.status)
	require.Equal(t, "IP_RATE_LIMIT_UNAVAILABLE", result.code)

	require.Empty(t, emailSender.Messages)
}

// The email budget fails the same way when only it is the one that cannot answer,
// which is the state a Redis outage looks like once the client budget has already
// been spent within its window.
func TestRegisterAPI_PinRateLimit_UnavailableReturns503(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	verificationID := openPendingRegistration(
		t,
		"ratelimit-unavailable@example.com",
	)

	limiter := testutil.UnavailablePinRateLimiter(
		errors.New("dial tcp: connection refused"),
	).AllowNamespace(testutil.PinIPNamespace, testutil.NamespaceOutcome{
		Allowed: true,
	})

	app, emailSender := testutil.NewAppWithLimiter(testPool, limiter)

	result := postPinEndpoint(t, app, verificationID, pinPath)
	require.Equal(t, http.StatusServiceUnavailable, result.status)
	require.Equal(t, registration.CodePinRateLimitUnavailable, result.code)

	result = postPinEndpoint(t, app, verificationID, resendPath)
	require.Equal(t, http.StatusServiceUnavailable, result.status)
	require.Equal(t, registration.CodePinRateLimitUnavailable, result.code)

	require.Empty(t, emailSender.Messages)
}

// A 503 has no known recovery time, so it must not carry a wait a client would
// believe and retry on.
func TestRegisterAPI_PinRateLimit_UnavailableCarriesNoRetryAfter(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	verificationID := openPendingRegistration(
		t,
		"ratelimit-no-retry-after@example.com",
	)

	limiter := testutil.UnavailablePinRateLimiter(
		errors.New("dial tcp: connection refused"),
	)

	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	result := postPinEndpoint(t, app, verificationID, pinPath)

	require.Equal(t, http.StatusServiceUnavailable, result.status)
	require.Empty(t, result.retryAfter)
}

// The default test app allows everything, so a registration test that is not
// about the limiter behaves exactly as it did before the limiter existed.
func TestRegisterAPI_PinRateLimit_WithinBudgetIsUnchanged(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))
	testEmailSender.Messages = nil

	verificationID := openPendingRegistration(
		t,
		"ratelimit-within@example.com",
	)

	result := postPinEndpoint(t, testApp, verificationID, pinPath)
	require.Equal(t, http.StatusCreated, result.status)
	require.Empty(t, result.code)

	require.Len(t, testEmailSender.Messages, 1)

	// The cooldown is untouched: a second issue right away is still refused, and
	// still with the code it always had.
	result = postPinEndpoint(t, testApp, verificationID, resendPath)
	require.Equal(t, http.StatusConflict, result.status)
	require.Equal(t, registration.CodeVerificationResendCooldown, result.code)

	require.Len(t, testEmailSender.Messages, 1)
}

// Both endpoints spend the same email budget. Ageing the cooldown is what lets
// the second endpoint reach the limiter at all; without it the cooldown would
// refuse first and this would prove nothing.
func TestRegisterAPI_PinRateLimit_BothEndpointsShareOneEmailBudget(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const email = "ratelimit-shared@example.com"

	verificationID := openPendingRegistration(t, email)

	limiter := testutil.NewCountingPinRateLimiter()

	app, emailSender := testutil.NewAppWithLimiter(testPool, limiter)

	result := postPinEndpoint(t, app, verificationID, pinPath)
	require.Equal(t, http.StatusCreated, result.status)

	require.NoError(
		t,
		db.MakeVerificationResendable(ctx, verificationID),
	)

	result = postPinEndpoint(t, app, verificationID, resendPath)
	require.Equal(t, http.StatusOK, result.status)

	require.Len(t, emailSender.Messages, 2)

	// Both spent the same subject, so the real limiter counts them together
	// rather than each endpoint having a budget of its own.
	emailSubjects := limiter.SubjectsIn(testutil.PinEmailNamespace)
	require.Len(t, emailSubjects, 2)
	require.Equal(t, emailSubjects[0], emailSubjects[1])

	// And each request also spent the client address budget, on the route's own
	// namespace, which is a separate counter rather than a second reading of the
	// email one.
	require.Len(t, limiter.SubjectsIn(testutil.PinIPNamespace), 2)
}

// The client address budget is the one in front of the handler, so it is what a
// caller meets first and what the two endpoints must share.
func TestRegisterAPI_IPRateLimit_BothEndpointsShareOneIPBudget(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	verificationID := openPendingRegistration(
		t,
		"ip-limit-shared@example.com",
	)

	limiter := testutil.NewCountingPinRateLimiter().
		AllowNamespace(testutil.PinIPNamespace, testutil.NamespaceOutcome{
			Allowed: false,
		})

	app, emailSender := testutil.NewAppWithLimiter(testPool, limiter)

	result := postPinEndpoint(t, app, verificationID, pinPath)
	require.Equal(t, http.StatusTooManyRequests, result.status)
	require.Equal(t, "IP_RATE_LIMIT_EXCEEDED", result.code)

	result = postPinEndpoint(t, app, verificationID, resendPath)
	require.Equal(t, http.StatusTooManyRequests, result.status)
	require.Equal(t, "IP_RATE_LIMIT_EXCEEDED", result.code)

	// Both requests spent the IP budget and neither reached the email budget.
	// That is the ordering property: a request the client limit refuses cannot
	// also spend the per-address budget behind it.
	require.Equal(t, 2, limiter.CallsIn(testutil.PinIPNamespace))
	require.Zero(
		t,
		limiter.CallsIn(testutil.PinEmailNamespace),
		"a request refused by the client budget must not reach the address budget",
	)

	require.Empty(t, emailSender.Messages)
}

// The client budget counts requests the endpoints go on to reject. A limit that
// only counted successes would be free to burn with the cheapest requests there
// are, and the cheapest request is one naming an id that does not exist.
func TestRegisterAPI_IPRateLimit_CountsRejectedRequests(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	limiter := testutil.NewCountingPinRateLimiter()

	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	// A well-formed request against a verification that does not exist: the
	// endpoint will reject it, and the budget must still have been spent.
	unknown := uuid.New()

	result := postPinEndpoint(t, app, unknown, pinPath)
	require.Equal(t, http.StatusNotFound, result.status)

	require.Equal(
		t,
		1,
		limiter.CallsIn(testutil.PinIPNamespace),
		"a request the endpoint rejects is still a request against the budget",
	)

	// A malformed id never becomes a request at all, so it cannot be counted.
	// This is the boundary of the property, and it is worth pinning: the budget
	// covers requests that reach these routes, not every packet sent at them.
	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/verification/not-a-uuid/pin",
		nil,
	)

	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// The other registration endpoints are not client-limited. Only the two that send
// a message are, and widening this would limit every caller by address whether or
// not they can do anything with it.
func TestRegisterAPI_IPRateLimit_DoesNotLimitRegistrationCreation(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	limiter := testutil.ExceededPinRateLimiter()

	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	for _, email := range []string{
		"create-a@example.com",
		"create-b@example.com",
		"create-c@example.com",
	} {
		req := httptest.NewRequest(
			http.MethodPost,
			"/auth/register/manual",
			strings.NewReader(`{"email":"`+email+`"}`),
		)
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(
			t,
			http.StatusCreated,
			resp.StatusCode,
			"creating a registration sends nothing and must not be limited",
		)
	}

	require.Empty(
		t,
		limiter.Subjects,
		"registration creation must not consult the limiter at all",
	)
}

// A caller cannot get a second budget by claiming a different address, because
// nothing about the address is believed until something has been configured to
// say who is allowed to set it.
func TestRegisterAPI_IPRateLimit_SpoofedHeadersCannotBypassTheLimit(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	verificationID := openPendingRegistration(
		t,
		"ip-spoofing@example.com",
	)

	limiter := testutil.NewCountingPinRateLimiter()

	// The default app trusts no proxy, so every request presents the same peer
	// however it asks to be identified.
	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	spoofed := []string{
		"198.51.100.1",
		"198.51.100.2",
		"198.51.100.3",
		"203.0.113.7",
		"2001:db8::1",
	}

	for _, address := range spoofed {
		req := httptest.NewRequest(
			http.MethodPost,
			"/auth/register/verification/"+verificationID.String()+pinPath,
			nil,
		)
		req.Header.Set("X-Forwarded-For", address)
		req.Header.Set("X-Real-IP", address)
		req.Header.Set("CF-Connecting-IP", address)
		req.Header.Set("True-Client-IP", address)

		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		// Ageing the cooldown keeps this test about the client budget: what
		// refused these requests would otherwise be the per-verification
		// cooldown, which says nothing about addresses.
		require.NoError(
			t,
			db.MakeVerificationResendable(ctx, verificationID),
		)
	}

	// Every one of them landed on the peer's own address, so the budget was
	// spent once rather than five times.
	ipSubjects := limiter.SubjectsIn(testutil.PinIPNamespace)
	require.Len(t, ipSubjects, len(spoofed))

	for _, subject := range ipSubjects {
		require.Equal(
			t,
			"0.0.0.0",
			subject,
			"a spoofed header must not move the client address",
		)
	}
}

// The IP budget and the email budget are independent counters, so exhausting
// one must not spend the other.
func TestRegisterAPI_IPRateLimit_DoesNotConsumeTheEmailBudget(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	verificationID := openPendingRegistration(
		t,
		"ip-not-email@example.com",
	)

	limiter := testutil.NewCountingPinRateLimiter()

	app, emailSender := testutil.NewAppWithLimiter(testPool, limiter)

	// Enough requests to be more than the email budget would allow.
	for range registration.PinRateLimitMax + 2 {
		req := httptest.NewRequest(
			http.MethodPost,
			"/auth/register/verification/"+verificationID.String()+pinPath,
			nil,
		)

		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		require.NoError(
			t,
			db.MakeVerificationResendable(ctx, verificationID),
			"the cooldown, not the budget, is what should be limiting here",
		)
	}

	// The email budget was spent once per request that actually reached the
	// service, and the IP budget once per request that reached the route.
	require.Equal(
		t,
		registration.PinRateLimitMax+2,
		limiter.CallsIn(testutil.PinEmailNamespace),
	)
	require.Equal(
		t,
		registration.PinRateLimitMax+2,
		limiter.CallsIn(testutil.PinIPNamespace),
	)

	require.Len(t, emailSender.Messages, registration.PinRateLimitMax+2)
}

// The retry delay a refused caller receives must be a whole number of seconds a
// client can put on a timer.
func TestRegisterAPI_IPRateLimit_RetryAfterIsANumber(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	verificationID := openPendingRegistration(
		t,
		"ip-retry-after@example.com",
	)

	limiter := testutil.NewCountingPinRateLimiter().
		AllowNamespace(testutil.PinIPNamespace, testutil.NamespaceOutcome{
			Allowed:    false,
			RetryAfter: 512 * time.Second,
		})

	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	result := postPinEndpoint(t, app, verificationID, pinPath)
	require.Equal(t, http.StatusTooManyRequests, result.status)
	require.Equal(t, "512", result.retryAfter)

	seconds, err := strconv.Atoi(result.retryAfter)
	require.NoError(t, err)
	require.Positive(t, seconds)
	require.LessOrEqual(t, seconds, int(registration.PinIPRateLimitWindow.Seconds()))
}
