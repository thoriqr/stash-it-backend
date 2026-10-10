package login_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
)

// The per-client-address budgets mounted in front of the authentication routes.
//
// What this file pins is the wiring, which nothing below the route can: that
// POST /login and all three Google routes spend a budget before the handler sees
// the request, that each spends it under the policy the feature declares rather
// than one the route invented, and that the two flows spend budgets that are
// counted separately. The budget's own behaviour — counting, windows, atomicity —
// belongs to internal/ratelimit, and the service's use of the manual-login
// per-address budget is pinned in login_rate_limit_service_test.go.
//
// Every request below is deliberately malformed. A request the handler refuses
// costs nothing, so a test that floods the budget does not also pay for an
// Argon2id derivation or a Google token verification, and the 400 a
// well-formed-looking request would produce is exactly the marker that
// distinguishes "the limiter let it through" from "the limiter stopped it".

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

	return postAt(t, app, http.MethodPost, "/auth/login", "this is not json")
}

// postAt sends a request whose body cannot be parsed, so the handler answers 400
// when it is reached and does nothing at all when it is not.
//
// This is what makes it usable against every route at once. The limiter is
// mounted in front of body binding, of path parsing and of token verification, so
// a request the handler refuses is still counted — and a marker of exactly that
// kind is what tells the two outcomes apart. A 400 says the limiter let the
// request through; a 429 says it stopped the request before the handler.
func postAt(
	t *testing.T,
	app *fiber.App,
	method string,
	path string,
	requestBody string,
) loginRouteOutcome {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(requestBody))
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

// googleAuthPaths are the three routes that spend the Google authentication
// budget.
//
// All three are named in every test below rather than one representative path,
// because the budget is shared and a wiring mistake on any one of them would be
// invisible if only another were exercised.
func googleAuthPaths() []struct {
	method string
	path   string
} {
	return []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/auth/login/google"},
		{http.MethodGet, "/auth/login/google/account-link/not-a-uuid"},
		{
			http.MethodPost,
			"/auth/login/google/account-link/not-a-uuid/confirm",
		},
	}
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

// Each Google route spends the Google budget, under the policy the feature
// declares and against the namespace it declares, before the handler runs.
//
// The policy is asserted rather than merely exercised because the route is the
// only place it is chosen: a policy the service also declared, or one the route
// hardcoded, is a second answer to a question that must have exactly one. The
// namespace is asserted for the same reason, and because it is the fact that
// keeps this budget from being the manual-login one.
func TestRoutes_GoogleAuth_WithinBudgetReachesTheHandler(t *testing.T) {
	for _, route := range googleAuthPaths() {
		t.Run(route.path, func(t *testing.T) {
			limiter := testutil.NewCountingPinRateLimiter()

			app := newLoginRoutesApp(t, limiter)

			outcome := postAt(t, app, route.method, route.path, "this is not json")

			require.Equal(
				t,
				http.StatusBadRequest,
				outcome.status,
				"the limiter let the request through and the handler refused the "+
					"request: %s", outcome.code,
			)

			require.Equal(
				t,
				[]ratelimit.Policy{{
					Max:    login.GoogleAuthIPRateLimitMax,
					Window: login.GoogleAuthIPRateLimitWindow,
				}},
				limiter.Policies,
			)

			// The subject is the address the framework resolved, canonicalized,
			// exactly as it is for manual login.
			subjects := limiter.SubjectsIn(testutil.GoogleAuthIPNamespace)
			require.Len(t, subjects, 1)

			normalized, ok := ratelimit.NormalizeIP(subjects[0])
			require.True(t, ok, "the subject must be an address the framework resolved")
			require.Equal(
				t,
				normalized,
				subjects[0],
				"the subject must already be canonical",
			)

			// Neither manual-login budget was touched. Google login has no
			// password to charge, and this route spent nothing else.
			require.Empty(t, limiter.SubjectsIn(testutil.LoginEmailNamespace))
			require.Zero(t, limiter.ChargeFor(testutil.LoginIPNamespace))
		})
	}
}

// One budget covers the whole Google flow, so moving between its routes does not
// hand a caller a fresh allowance.
//
// This is the property that makes the three mounts one middleware instance rather
// than three identical ones. A caller refused on Google login is not someone who
// should be given another sixty requests by asking for the confirmation instead.
func TestRoutes_GoogleAuth_AllThreeRoutesSpendOneBudget(t *testing.T) {
	limiter := testutil.ExceededPinRateLimiter()

	app := newLoginRoutesApp(t, limiter)

	for _, route := range googleAuthPaths() {
		outcome := postAt(t, app, route.method, route.path, "this is not json")

		require.Equal(
			t,
			http.StatusTooManyRequests,
			outcome.status,
			"%s %s is not behind the Google budget", route.method, route.path,
		)
	}

	// Every route was charged against the one namespace, and the manual-login
	// budget was not consulted at all.
	require.Equal(
		t,
		[]string{
			testutil.GoogleAuthIPNamespace,
			testutil.GoogleAuthIPNamespace,
			testutil.GoogleAuthIPNamespace,
		},
		limiter.Namespaces,
	)
	require.Equal(
		t,
		3,
		limiter.CallsIn(testutil.GoogleAuthIPNamespace),
	)
	require.Zero(t, limiter.CallsIn(testutil.LoginIPNamespace))
}

// A spent client budget stops the request in front of the handler.
//
// This is what the budget is worth on a route that verifies a Google token and
// one that writes sessions: a body the handler would refuse anyway is still
// counted, so the cheapest request an attacker can send is not free.
func TestRoutes_GoogleAuth_SpentBudgetStopsBeforeTheHandler(t *testing.T) {
	for _, route := range googleAuthPaths() {
		t.Run(route.path, func(t *testing.T) {
			limiter := testutil.ExceededPinRateLimiter()

			app := newLoginRoutesApp(t, limiter)

			outcome := postAt(t, app, route.method, route.path, "this is not json")

			require.Equal(t, http.StatusTooManyRequests, outcome.status)
			require.Equal(t, "IP_RATE_LIMIT_EXCEEDED", outcome.code)

			// A refusal has to carry a wait, or a client cannot tell when to come
			// back.
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
				int(login.GoogleAuthIPRateLimitWindow.Seconds()),
			)

			// A refusal is counted, and it reaches no second budget: there is none
			// in front of these routes, and the one behind them belongs to manual
			// login.
			require.Empty(t, limiter.SubjectsIn(testutil.LoginEmailNamespace))
			require.Zero(t, limiter.ChargeFor(testutil.LoginIPNamespace))
		})
	}
}

// A limiter that cannot answer has not said the request is within budget. Allowing
// it would hand anyone the ability to remove the ceiling by causing an outage —
// and on these routes it would mean an unverified ID token was checked anyway,
// which is the whole thing the budget stands in front of.
func TestRoutes_GoogleAuth_UnavailableBudgetFailsClosed(t *testing.T) {
	for _, route := range googleAuthPaths() {
		t.Run(route.path, func(t *testing.T) {
			limiter := testutil.UnavailablePinRateLimiter(
				errors.New("dial tcp: connection refused"),
			)

			app := newLoginRoutesApp(t, limiter)

			outcome := postAt(t, app, route.method, route.path, "this is not json")

			require.Equal(t, http.StatusServiceUnavailable, outcome.status)
			require.Equal(t, "IP_RATE_LIMIT_UNAVAILABLE", outcome.code)

			// There is no window to wait out, so a wait would name a moment nobody
			// chose.
			require.Empty(t, outcome.retryAfter)

			require.Empty(t, limiter.SubjectsIn(testutil.LoginEmailNamespace))
			require.Zero(t, limiter.ChargeFor(testutil.LoginIPNamespace))
		})
	}
}

// Google login and manual login are two doors into the same application, not two
// rooms in it. Neither may spend the other's budget, and a refusal on one must
// not stop a caller using the other.
//
// The budget belongs to the client address rather than to the route, so this is
// the same fact the manual-login route is subject to, stated from the other side:
// the two namespaces are separate counters, and exhausting one says nothing about
// the other.
func TestRoutes_GoogleAuth_DoesNotSpendTheManualLoginBudget(t *testing.T) {
	// Only the Google budget is spent. If these routes reached the manual-login
	// one it would allow them, and a request refused here would prove nothing.
	limiter := testutil.NewCountingPinRateLimiter().
		AllowNamespace(testutil.GoogleAuthIPNamespace, testutil.NamespaceOutcome{
			Allowed:    false,
			RetryAfter: 42 * time.Second,
		})

	app := newLoginRoutesApp(t, limiter)

	for _, route := range googleAuthPaths() {
		outcome := postAt(t, app, route.method, route.path, "this is not json")

		require.Equal(
			t,
			http.StatusTooManyRequests,
			outcome.status,
			"%s %s is not behind the Google budget", route.method, route.path,
		)
	}

	// The manual login budget was never consulted at all, so a Google flood costs
	// a password user nothing.
	require.Zero(t, limiter.CallsIn(testutil.LoginIPNamespace))
	require.Zero(t, limiter.ChargeFor(testutil.LoginIPNamespace))
	require.Zero(t, limiter.ChargeFor(testutil.LoginEmailNamespace))
}

// The other direction: a caller whose manual-login budget is spent can still
// reach Google login, because the two are counted separately.
//
// A limiter that refuses both would make this test pass for the wrong reason —
// every route would be a 429 and nothing would distinguish "Google is independent"
// from "Google is simply also limited". Only one budget is spent here.
func TestRoutes_GoogleAuth_IsUnaffectedByTheManualLoginBudget(t *testing.T) {
	limiter := testutil.NewCountingPinRateLimiter().
		AllowNamespace(testutil.LoginIPNamespace, testutil.NamespaceOutcome{
			Allowed:    false,
			RetryAfter: 42 * time.Second,
		})

	app := newLoginRoutesApp(t, limiter)

	require.Equal(
		t,
		http.StatusTooManyRequests,
		postLogin(t, app).status,
		"the manual login budget is spent and must refuse",
	)

	for _, route := range googleAuthPaths() {
		outcome := postAt(t, app, route.method, route.path, "this is not json")

		require.Equal(
			t,
			http.StatusBadRequest,
			outcome.status,
			"a spent manual login budget refused %s %s: %s",
			route.method,
			route.path,
			outcome.code,
		)
	}

	require.Zero(t, limiter.ChargeFor(testutil.LoginEmailNamespace))
}
