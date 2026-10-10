package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	login "github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	logintestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/login/generated"
)

// The per-client-address budget covering the Google authentication routes,
// against real Redis.
//
// All three routes spend one budget, so a caller who has been refused on Google
// login cannot move to the account-link routes and start again. What that buys is
// a ceiling on cost rather than on guessing: the Google routes have no secret to
// try, but every request verifies a token against Google's published keys, and a
// caller holding one valid token can otherwise make the application write
// sessions and confirmations for as long as it keeps asking.
//
// Every request here is deliberately malformed. The budget is spent in front of
// the handler, so a request that never reaches a Google token verification, a
// path parameter or a database write is still counted — which is both the
// property under test and the reason a flood costs the test nothing.
//
// Isolation is the suite's existing convention: newIPLimitedApp clears every
// rl:* key before the app is built, so no counter from another test is visible.

// googleRateLimitPaths are the three routes that spend the Google budget.
//
// The ids are not UUIDs on purpose. Each limiter is mounted in front of path
// parsing, so these requests spend the budget in full and are then refused by the
// handler's own validation rather than by the limiter, which makes a 400 the
// unambiguous marker that the limiter let the request through.
func googleRateLimitPaths() []struct {
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

// googleRequest issues one malformed request to a Google route from the given
// client address.
//
// The forwarded header is only read because this app is configured to read it.
// Nothing about the address is believed on any other app.
func googleRequest(
	t *testing.T,
	app *fiber.App,
	method string,
	path string,
	address string,
) (*http.Response, error) {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader("this is not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", address)

	return app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
}

// postMalformedGoogle issues one malformed Google request and reads the outcome,
// including the error envelope on a refusal.
func postMalformedGoogle(
	t *testing.T,
	app *fiber.App,
	method string,
	path string,
	address string,
) loginResponse {
	t.Helper()

	resp, err := googleRequest(t, app, method, path, address)
	require.NoError(t, err)

	result := loginResponse{
		status:     resp.StatusCode,
		retryAfter: resp.Header.Get(fiber.HeaderRetryAfter),
	}

	if result.status < 400 {
		return result
	}

	require.NoError(t, decodeLoginError(t, resp, &result))

	return result
}

// postMalformedGoogleLogin issues one malformed request to the Google login route
// itself, which is where the flood below is spent.
func postMalformedGoogleLogin(
	t *testing.T,
	app *fiber.App,
	address string,
) loginResponse {
	t.Helper()

	return postMalformedGoogle(
		t,
		app,
		http.MethodPost,
		"/auth/login/google",
		address,
	)
}

// floodGoogleAuthIPBudget spends one address's whole Google budget and returns
// once it is exhausted.
func floodGoogleAuthIPBudget(t *testing.T, app *fiber.App, address string) {
	t.Helper()

	for attempt := range login.GoogleAuthIPRateLimitMax {
		result := postMalformedGoogleLogin(t, app, address)

		require.NotEqual(
			t,
			http.StatusTooManyRequests,
			result.status,
			"attempt %d should be within budget, got %s", attempt, result.code,
		)
	}

	result := postMalformedGoogleLogin(t, app, address)

	require.Equal(
		t,
		http.StatusTooManyRequests,
		result.status,
		"the budget should be spent after %d requests",
		login.GoogleAuthIPRateLimitMax,
	)
	require.Equal(t, "IP_RATE_LIMIT_EXCEEDED", result.code)
}

// The Google budget is walked end to end over real Redis: the Max'th request from
// one address is allowed and the next is refused, which is a statement about the
// counter's arithmetic rather than about the route that spent it.
//
// What this cannot show on its own is that the other two routes are behind the
// same counter; that is the next test.
func TestGoogleAuthIPRateLimit_ExhaustedBudgetRefuses(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	app, _ := newIPLimitedApp(t)

	const address = "198.51.100.80"

	floodGoogleAuthIPBudget(t, app, address)
}

// One budget covers the whole flow, so a caller refused on Google login is not
// handed a fresh allowance by asking for the confirmation instead.
//
// The budget belongs to the client address rather than to the route, which is the
// same fact the manual-login route is subject to; here it is what stops a single
// flood from being spread across three endpoints.
func TestGoogleAuthIPRateLimit_AllThreeRoutesSpendOneBudget(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	app, _ := newIPLimitedApp(t)

	const address = "198.51.100.81"

	// The whole allowance goes on one route.
	floodGoogleAuthIPBudget(t, app, address)

	// The other two are refused on the same counter. Were they behind budgets of
	// their own, these would be answered by their handlers.
	for _, route := range googleRateLimitPaths()[1:] {
		result := postMalformedGoogle(t, app, route.method, route.path, address)

		require.Equal(
			t,
			http.StatusTooManyRequests,
			result.status,
			"%s %s was given a fresh allowance: %s",
			route.method,
			route.path,
			result.code,
		)
	}
}

// A client's address is exhausted, and no other client's is. This is the whole
// reason the budget is per address: one person's flood must not stop everyone
// behind the same proxy, which matters more here than it does for manual login
// because a Google refusal leaves the user no way in at all.
func TestGoogleAuthIPRateLimit_OneAddressDoesNotSpendAnother(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	const flooder = "198.51.100.82"
	const neighbour = "198.51.100.83"

	app, _ := newIPLimitedApp(t)

	floodGoogleAuthIPBudget(t, app, flooder)

	// The neighbour is untouched: same proxy, same moment, same route.
	require.NotEqual(
		t,
		http.StatusTooManyRequests,
		postMalformedGoogleLogin(t, app, neighbour).status,
		"a neighbour was blocked by another client's flood",
	)

	// And the flooded address is still refused, so nothing was quietly restored.
	require.Equal(
		t,
		http.StatusTooManyRequests,
		postMalformedGoogleLogin(t, app, flooder).status,
	)
}

// The refused response carries a wait a client can act on, and it is derived from
// the counter rather than from the configured window.
func TestGoogleAuthIPRateLimit_ExceededCarriesRetryAfter(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	app, _ := newIPLimitedApp(t)

	const address = "198.51.100.84"

	floodGoogleAuthIPBudget(t, app, address)

	var waits []int

	// A few refusals in a row. Each must report a wait, and none may extend the
	// window: a refused request spends budget the window already had.
	for range 3 {
		result := postMalformedGoogleLogin(t, app, address)

		require.Equal(t, http.StatusTooManyRequests, result.status)
		require.NotEmpty(
			t,
			result.retryAfter,
			"a 429 must tell the client when to come back",
		)

		seconds, err := strconv.Atoi(result.retryAfter)
		require.NoError(
			t,
			err,
			"Retry-After must be a whole number of seconds, got %q",
			result.retryAfter,
		)
		require.Positive(t, seconds)
		require.LessOrEqual(
			t,
			seconds,
			int(login.GoogleAuthIPRateLimitWindow.Seconds()),
			"the wait must never exceed the window it came from",
		)

		waits = append(waits, seconds)
	}

	// The window is not pushed forward by the requests being refused, so the
	// reported wait can only hold steady or fall. A wait that climbed would mean a
	// refused caller could hold the counter open indefinitely.
	for index := 1; index < len(waits); index++ {
		require.LessOrEqual(
			t,
			waits[index],
			waits[index-1],
			"a refused request must not extend the window: waits %v", waits,
		)
	}
}

// The budget is atomic across concurrent requests from one address. A client that
// fires its requests in parallel must not get more than its allowance by having
// several in flight at once.
//
// This is the property the fake limiter cannot prove, and the reason the budget is
// charged rather than checked: with only a read, every request in a burst would
// read "within budget" and every one of them would go on to verify a token.
func TestGoogleAuthIPRateLimit_ConcurrentRequestsCannotExceedTheBudget(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	app, _ := newIPLimitedApp(t)

	const (
		address = "198.51.100.85"
		// Comfortably more than the budget, so an over-admission is visible
		// rather than masked by requests that would be refused anyway.
		attempts = login.GoogleAuthIPRateLimitMax * 3
	)

	var refused atomic.Int64

	// Every goroutine signals that it is ready and then blocks. The test releases
	// them together, so the requests leave as close to simultaneously as the
	// scheduler allows rather than trickling out as each one is scheduled. That is
	// what makes an over-admission visible: a limiter admitting by
	// read-modify-write would let several in flight past the limit together.
	start := make(chan struct{})

	var ready sync.WaitGroup
	var done sync.WaitGroup

	ready.Add(attempts)
	done.Add(attempts)

	for range attempts {
		go func() {
			defer done.Done()

			ready.Done()
			<-start

			result := postMalformedGoogleLogin(t, app, address)

			if result.status == http.StatusTooManyRequests {
				refused.Add(1)
			}
		}()
	}

	ready.Wait()
	close(start)
	done.Wait()

	require.Equal(
		t,
		int64(attempts-login.GoogleAuthIPRateLimitMax),
		refused.Load(),
		"a client must not exceed its budget by sending in parallel",
	)
}

// Google login and manual login are counted separately. A caller who has spent
// the Google budget can still sign in with a password, and a caller who has spent
// the manual budget can still sign in with Google.
//
// This is the fact the two namespaces exist for. One flow's flood must cost the
// other flow's users nothing, and a user whose budget was spent on one door has
// not thereby spent it on the other.
func TestGoogleAuthIPRateLimit_DoesNotSpendTheManualLoginBudget(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	const address = "198.51.100.86"

	app, _ := newIPLimitedApp(t)

	floodGoogleAuthIPBudget(t, app, address)

	// The manual login budget is a separate counter and is untouched, so the whole
	// allowance is still there.
	result := postMalformedLogin(t, app, address)

	require.NotEqual(
		t,
		http.StatusTooManyRequests,
		result.status,
		"the Google routes spent the manual login budget: %s", result.code,
	)

	require.Equal(
		t,
		http.StatusBadRequest,
		result.status,
		"the manual login route should have reached its handler",
	)
}

// The other direction, over the same real counters.
//
// It matters more than it looks: an application whose limiter fails closed, as
// this one does, must not be able to take both doors down with one exhausted
// counter. That is asserted here by spending the manual-login budget and then
// showing the Google route is still answered by its own.
func TestGoogleAuthIPRateLimit_ManualLoginBudgetDoesNotBlockGoogleLogin(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	const address = "198.51.100.87"

	app, _ := newIPLimitedApp(t)

	floodLoginIPBudget(t, app, address)

	// The manual budget is spent and refusing...
	require.Equal(
		t,
		http.StatusTooManyRequests,
		postMalformedLogin(t, app, address).status,
	)

	// ...and Google login is still answered by its own counter, so it reaches its
	// handler rather than being refused by the other flow's exhaustion.
	require.Equal(
		t,
		http.StatusBadRequest,
		postMalformedGoogleLogin(t, app, address).status,
		"a spent manual login budget refused Google login",
	)
}
