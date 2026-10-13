package integration_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/auth"
	"github.com/thoriqr/stash-it-backend/internal/api/search"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
)

// The per-user budget in front of GET /search, against real Redis.
//
// A fake would prove the wiring and nothing else. The properties that matter here
// are that a refused search never reaches the query it guards, that a burst from
// one account cannot jointly exceed the allowance, and that the budget is keyed on
// the account rather than on the address. A read-modify-write counter would admit
// a whole burst together, and a fake is exactly the thing that would hide that.
//
// Nothing here seeds saved items. Search is user-scoped, so a token for an
// account with no rows is answered with an empty result rather than an error, and
// the budget is charged before the handler runs. That keeps these tests about the
// limiter rather than about query cost.

const searchRateLimitSecret = "search-user-rate-limit-test-secret"

// errRedisUnavailable stands in for the counter being unreachable. Only the
// limiter's decision to report it as a refusal is under test; that the production
// limiter reports it at all is covered by TestRateLimiter_UnreachableRedisFailsClosed.
var errRedisUnavailable = errors.New("redis is unreachable")

// searchTooShortQuery is below QueryMinLength, so the handler would reject it
// with 400 INVALID_SEARCH_QUERY if it ever ran.
//
// It is what makes "the handler did not execute" observable on a read-only route:
// without something the handler would itself refuse, a 429 would be
// indistinguishable from a search that happened and was then rated.
const searchTooShortQuery = "x"

// searchValidQuery is a well-formed query the handler accepts.
const searchValidQuery = "database"

type searchOutcome struct {
	status     int
	body       string
	retryAfter string
}

// newSearchRateLimitApp builds the app over the shared test Redis with its limiter
// keys cleared, so one test cannot see another's counts.
func newSearchRateLimitApp(
	t *testing.T,
	limiter auth.RateLimiter,
) *fiber.App {
	t.Helper()

	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	return app
}

// newSearchRealLimiter returns the production limiter over the shared test Redis
// with its keys cleared, so a test exercises the real atomic counter.
func newSearchRealLimiter(t *testing.T) *ratelimit.Limiter {
	t.Helper()

	client, err := testutil.StartRedisForTests(t.Context())
	require.NoError(t, err)

	keys, err := client.Keys(t.Context(), "rl:*").Result()
	require.NoError(t, err)

	if len(keys) > 0 {
		require.NoError(t, client.Del(t.Context(), keys...).Err())
	}

	return ratelimit.New(client, []byte(searchRateLimitSecret))
}

// searchFrom runs one search for the given token.
func searchFrom(
	t *testing.T,
	app *fiber.App,
	token string,
	query string,
) searchOutcome {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodGet,
		"/search?q="+query,
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	require.NoError(t, err)

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return searchOutcome{
		status:     resp.StatusCode,
		body:       string(raw),
		retryAfter: resp.Header.Get(fiber.HeaderRetryAfter),
	}
}

// Below the threshold searches are served normally. Above it the caller is told to
// wait, and the wait comes from the counter's own remaining window rather than
// from the key's garbage-collection TTL.
func TestSearchUserRateLimit_ExhaustionReturns429WithRetryAfter(t *testing.T) {
	app := newSearchRateLimitApp(t, newSearchRealLimiter(t))

	userID := uuid.New()
	token := newTestAccessToken(t, userID)

	for attempt := 1; attempt <= search.SearchUserRateLimitMax; attempt++ {
		outcome := searchFrom(t, app, token, searchValidQuery)

		require.Equal(
			t,
			http.StatusOK,
			outcome.status,
			"attempt %d of %d was refused before the budget was spent: %s",
			attempt,
			search.SearchUserRateLimitMax,
			outcome.body,
		)
	}

	outcome := searchFrom(t, app, token, searchValidQuery)

	require.Equal(t, http.StatusTooManyRequests, outcome.status)
	require.Contains(t, outcome.body, middleware.CodeUserRateLimitExceeded)

	// Being refused spends budget rather than extending the window, so an account
	// cannot punish the deployment by keeping trying.
	require.Equal(
		t,
		http.StatusTooManyRequests,
		searchFrom(t, app, token, searchValidQuery).status,
	)

	// Retry-After is read from the counter's remaining life. The window is one
	// minute, so the wait must be positive and must not exceed it.
	require.NotEmpty(t, outcome.retryAfter, "a 429 must carry a Retry-After")

	seconds, err := strconv.Atoi(outcome.retryAfter)
	require.NoError(t, err)
	require.Greater(t, seconds, 0)
	require.LessOrEqual(
		t,
		time.Duration(seconds)*time.Second,
		search.SearchUserRateLimitWindow,
	)
}

// A refusal must happen before the handler, so the query it guards never runs.
//
// Search is a read, so "nothing was written" would be true either way and would
// prove nothing. The observable fact is the response instead: the query used here
// is one the handler would itself refuse. While the budget lasts, the caller gets
// the handler's 400; once it is spent, the same request gets a 429 that the
// handler never produced. The change in which layer answered is the proof.
func TestSearchUserRateLimit_RefusedRequestNeverReachesTheHandler(t *testing.T) {
	app := newSearchRateLimitApp(t, newSearchRealLimiter(t))

	userID := uuid.New()
	token := newTestAccessToken(t, userID)

	// Before the budget is spent the handler answers, and its own rejection is
	// what the caller sees. This is the control for the assertion below.
	within := searchFrom(t, app, token, searchTooShortQuery)

	require.Equal(t, http.StatusBadRequest, within.status)
	require.Contains(t, within.body, search.CodeInvalidSearchQuery)

	for attempt := 1; attempt <= search.SearchUserRateLimitMax; attempt++ {
		searchFrom(t, app, token, searchValidQuery)
	}

	// The same request the handler just refused is now refused by the limiter, and
	// carries the limiter's code rather than the handler's.
	after := searchFrom(t, app, token, searchTooShortQuery)

	require.Equal(
		t,
		http.StatusTooManyRequests,
		after.status,
		"an exhausted budget must be refused before the handler runs",
	)
	require.Contains(t, after.body, middleware.CodeUserRateLimitExceeded)
	require.NotContains(t, after.body, search.CodeInvalidSearchQuery)
}

// A budget that cannot be evaluated is a refusal, not permission, and it is
// refused before the handler for the same reason an exhausted budget is.
//
// The same query that the handler would reject is used again, so the assertion
// also shows which layer answered: a 503 with the limiter's code is the limiter
// having stopped the request, not the handler having run and failed.
//
// The app is handed a limiter that cannot answer rather than a broken connection.
// `TestRateLimiter_UnreachableRedisFailsClosed` already proves the production
// limiter reports an error when Redis is gone, so what is under test here is the
// route's behaviour given that error.
func TestSearchUserRateLimit_UnavailableRedisRefusesBeforeTheHandler(t *testing.T) {
	app := newSearchRateLimitApp(
		t,
		testutil.UnavailablePinRateLimiter(errRedisUnavailable),
	)

	token := newTestAccessToken(t, uuid.New())

	outcome := searchFrom(t, app, token, searchTooShortQuery)

	require.Equal(t, http.StatusServiceUnavailable, outcome.status)
	require.Contains(t, outcome.body, middleware.CodeUserRateLimitUnavailable)

	// The handler's own rejection is absent, so it never ran.
	require.NotContains(t, outcome.body, search.CodeInvalidSearchQuery)

	// An unusable budget carries no Retry-After: there is no window to name, and
	// telling the caller to wait out one nobody spent would be a lie.
	require.Empty(t, outcome.retryAfter, "an unusable budget must not report a wait")
}

// The budget is keyed on the account. Which namespace each charge lands on is
// asserted from the limiter's own accounting rather than from response codes: a
// request refused by the wrong budget would still be a 429, and only the namespace
// shows which counter was spent.
func TestSearchUserRateLimit_ChargesOnlyItsOwnNamespace(t *testing.T) {
	limiter := testutil.NewCountingPinRateLimiter()

	app := newSearchRateLimitApp(t, limiter)

	outcome := searchFrom(
		t,
		app,
		newTestAccessToken(t, uuid.New()),
		searchValidQuery,
	)

	require.Equal(t, http.StatusOK, outcome.status)

	require.Equal(t, 1, limiter.CallsIn(testutil.SearchUserNamespace))

	// The saved-item budgets are other flows entirely and must not be touched.
	require.Zero(t, limiter.CallsIn(testutil.SavedItemCreateUserNamespace))
	require.Zero(t, limiter.CallsIn(testutil.SavedItemEnrichUserNamespace))

	// Nor any authentication budget: reaching search is not an authentication
	// attempt, and an exhausted search budget must never be a way to lock anyone
	// out of signing in.
	require.Zero(t, limiter.CallsIn(testutil.LoginIPNamespace))
	require.Zero(t, limiter.CallsIn(testutil.LoginEmailNamespace))
	require.Zero(t, limiter.CallsIn(testutil.GoogleAuthIPNamespace))
	require.Zero(t, limiter.CallsIn(testutil.RefreshIPNamespace))
}

// One account's flood must not spend another's allowance. This is the reason the
// subject is the account: the rows being scanned belong to whoever is searching,
// and keying on the address would have charged a shared address for all of them.
func TestSearchUserRateLimit_OneUserDoesNotSpendAnother(t *testing.T) {
	app := newSearchRateLimitApp(t, newSearchRealLimiter(t))

	exhaustedToken := newTestAccessToken(t, uuid.New())
	otherToken := newTestAccessToken(t, uuid.New())

	for range search.SearchUserRateLimitMax {
		searchFrom(t, app, exhaustedToken, searchValidQuery)
	}

	require.Equal(
		t,
		http.StatusTooManyRequests,
		searchFrom(t, app, exhaustedToken, searchValidQuery).status,
	)

	// A different account, sharing nothing but the process.
	require.Equal(
		t,
		http.StatusOK,
		searchFrom(t, app, otherToken, searchValidQuery).status,
		"an exhausted search budget locked out another user",
	)
}

// A read-modify-write counter would admit a whole burst past the limit together.
// Only the real Redis's atomic increment can show that it does not, so this is the
// one property in this file a fake could never establish.
func TestSearchUserRateLimit_ConcurrentBurstCannotExceedTheLimit(t *testing.T) {
	app := newSearchRateLimitApp(t, newSearchRealLimiter(t))

	token := newTestAccessToken(t, uuid.New())

	// Comfortably more than the allowance, all released at once.
	const burst = search.SearchUserRateLimitMax + 20

	var admitted atomic.Int64
	var refused atomic.Int64

	runConcurrently(t, burst, func(_ int) {
		if searchFrom(t, app, token, searchValidQuery).status ==
			http.StatusTooManyRequests {
			refused.Add(1)

			return
		}

		admitted.Add(1)
	})

	require.Equal(
		t,
		int64(search.SearchUserRateLimitMax),
		admitted.Load(),
		"more searches were admitted than the budget allows",
	)
	require.Equal(
		t,
		int64(burst-search.SearchUserRateLimitMax),
		refused.Load(),
	)
}

// The limiter is mounted after Auth, so a request without valid claims is refused
// by Auth and never reaches the budget. Keying on the identity is what requires
// that order: there is no subject to charge before Auth has run.
func TestSearchUserRateLimit_UnauthenticatedRequestSpendsNoBudget(t *testing.T) {
	limiter := testutil.NewCountingPinRateLimiter()

	app := newSearchRateLimitApp(t, limiter)

	req := httptest.NewRequest(
		http.MethodGet,
		"/search?q="+searchValidQuery,
		nil,
	)

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	require.NoError(t, err)

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	require.Zero(
		t,
		limiter.CallsIn(testutil.SearchUserNamespace),
		"an unauthenticated request must not spend a user's budget",
	)
}
