package login_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
)

// The per-client-address budget in front of manual login.
//
// What this file pins is the wiring, which nothing below the route can: that
// POST /login spends a budget before the handler sees the request, that it spends
// it under the policy the feature declares rather than one the route invented,
// and that it is not spent by the Google routes. The budget's own behaviour —
// counting, windows, atomicity — belongs to internal/ratelimit, and the service's
// use of the per-address budget is pinned in login_rate_limit_service_test.go.
//
// Every request below is deliberately malformed. A body the handler refuses costs
// nothing, so a test that floods the budget does not also pay for Argon2id
// derivations, and the 400 a well-formed-looking request would produce is exactly
// the marker that distinguishes "the limiter let it through" from "the limiter
// stopped it".

type loginRouteOutcome struct {
	status     int
	code       string
	retryAfter string
}

// newLoginRoutesApp mounts the feature's routes over a limiter the test controls.
//
// The service behind the handler is never reached by anything sent here, so its
// dependencies are the ones newTestService builds and no expectation is set on
// them: gomock would fail the test if the service were ever actually called, which
// is a second, independent statement of what a refusal has to prevent.
func newLoginRoutesApp(t *testing.T, limiter *testutil.CountingPinRateLimiter) *fiber.App {
	t.Helper()

	service, _, _, _, _, _ := newTestService(t)

	app := fiber.New(fiber.Config{
		ErrorHandler: httpx.NewErrorHandler(zap.NewNop()),
	})

	login.Routes(app.Group("/auth"), login.NewHandler(service), limiter)

	return app
}

// postLogin sends a body that cannot be parsed, so the handler answers 400 when it
// is reached and does nothing at all when it is not.
func postLogin(t *testing.T, app *fiber.App) loginRouteOutcome {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/login",
		strings.NewReader("this is not json"),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	require.NoError(t, err)

	outcome := loginRouteOutcome{
		status:     resp.StatusCode,
		retryAfter: resp.Header.Get(fiber.HeaderRetryAfter),
	}

	if outcome.status < 400 {
		return outcome
	}

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `error:"error"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	outcome.code = body.Error.Code

	return outcome
}

// A request within budget reaches the handler, and spends the route's own
// namespace under the policy the feature declares.
//
// The policy is asserted rather than merely exercised because the route is the
// only place it is chosen: a policy the service also declares, or one the route
// hardcodes, is a second answer to a question that must have exactly one.
func TestRoutes_LoginManual_WithinBudgetReachesTheHandler(t *testing.T) {
	limiter := testutil.NewCountingPinRateLimiter()

	app := newLoginRoutesApp(t, limiter)

	outcome := postLogin(t, app)

	require.Equal(
		t,
		http.StatusBadRequest,
		outcome.status,
		"the limiter let the request through and the handler refused the body: %s",
		outcome.code,
	)

	require.Equal(
		t,
		[]ratelimit.Policy{{
			Max:    login.LoginIPRateLimitMax,
			Window: login.LoginIPRateLimitWindow,
		}},
		limiter.Policies,
	)

	// The subject is the address the framework resolved, canonicalized — never
	// the raw string — because one client behind one NAT must not hold a budget
	// per spelling of its address. `app.Test` presents a single peer, so exactly
	// one subject is expected and it must be a canonical address rather than a
	// host:port pair.
	subjects := limiter.SubjectsIn(testutil.LoginIPNamespace)
	require.Len(t, subjects, 1)

	normalized, ok := ratelimit.NormalizeIP(subjects[0])
	require.True(t, ok, "the subject must be an address the framework resolved")
	require.Equal(t, normalized, subjects[0], "the subject must already be canonical")

	// Nothing was charged against the per-address budget. That one is spent by
	// the service, and a request that never reached it spent nothing.
	require.Empty(t, limiter.SubjectsIn(testutil.LoginEmailNamespace))
}

// A spent client budget stops the request in front of the handler.
//
// This is the property that makes the budget worth having where it is: a body the
// handler would have refused anyway is still counted, and so is every other shape
// of request that never reaches an authentication. A limit enforced inside the
// service would have counted none of them, because the cheapest request an
// attacker can send is one that cannot be authenticated.
func TestRoutes_LoginManual_SpentBudgetStopsBeforeTheHandler(t *testing.T) {
	limiter := testutil.ExceededPinRateLimiter()

	app := newLoginRoutesApp(t, limiter)

	outcome := postLogin(t, app)

	require.Equal(t, http.StatusTooManyRequests, outcome.status)
	require.Equal(t, "IP_RATE_LIMIT_EXCEEDED", outcome.code)

	// A refusal has to carry a wait, or a client cannot tell when to come back.
	require.NotEmpty(t, outcome.retryAfter)

	seconds, err := strconv.Atoi(outcome.retryAfter)
	require.NoError(
		t,
		err,
		"Retry-After must be a whole number of seconds, got %q",
		outcome.retryAfter,
	)
	require.Positive(t, seconds)
	require.LessOrEqual(
		t,
		seconds,
		int(login.LoginIPRateLimitWindow.Seconds()),
	)

	require.Empty(
		t,
		limiter.SubjectsIn(testutil.LoginEmailNamespace),
		"a request the client budget refused must not spend a second budget",
	)
}

// A limiter that cannot answer has not said the request is within budget. Allowing
// it would hand anyone the ability to remove the ceiling by causing an outage.
func TestRoutes_LoginManual_UnavailableBudgetFailsClosed(t *testing.T) {
	limiter := testutil.UnavailablePinRateLimiter(
		errors.New("dial tcp: connection refused"),
	)

	app := newLoginRoutesApp(t, limiter)

	outcome := postLogin(t, app)

	require.Equal(t, http.StatusServiceUnavailable, outcome.status)
	require.Equal(t, "IP_RATE_LIMIT_UNAVAILABLE", outcome.code)

	// There is no window to wait out, so a wait would name a moment nobody chose.
	require.Empty(t, outcome.retryAfter)

	require.Empty(t, limiter.SubjectsIn(testutil.LoginEmailNamespace))
}

// Google login is not behind this budget, and the reason it can be left off is
// that it has no password to guess: the credential is verified against the
// provider. Charging an authentication budget there would limit a user for
// something they did not do wrong.
//
// The request names an id that is not one, and the request is what a probing
// client sends. The limiter is mounted in front of path parsing, so a route that
// had been given the middleware would have spent the budget before discovering
// the id was unusable — which is exactly the case a limiter mounted anywhere else
// would have missed.
func TestRoutes_LoginGoogle_DoesNotSpendTheClientBudget(t *testing.T) {
	limiter := testutil.ExceededPinRateLimiter()

	app := newLoginRoutesApp(t, limiter)

	const confirmationID = "not-a-uuid"

	for _, path := range []string{
		"/auth/login/google",
		"/auth/login/google/account-link/" + confirmationID,
		"/auth/login/google/account-link/" + confirmationID + "/confirm",
	} {
		req := httptest.NewRequest(
			http.MethodPost,
			path,
			strings.NewReader("this is not json"),
		)
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		require.NoError(t, err)
		require.NotEqual(
			t,
			http.StatusTooManyRequests,
			resp.StatusCode,
			"%s was refused by a budget it is not behind", path,
		)
	}

	require.Empty(
		t,
		limiter.Namespaces,
		"no route other than manual login may spend a limiter budget",
	)
}
