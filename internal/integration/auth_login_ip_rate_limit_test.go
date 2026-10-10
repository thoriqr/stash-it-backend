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
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	logintestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/login/generated"
)

// The per-client-address budget in front of manual login, against real Redis.
//
// The fake limiter proves the wiring; only a real counter proves that concurrent
// requests from one address cannot jointly exceed the allowance. That property is
// the whole reason the budget is charged in front of the handler: it has to bound
// how much Argon2id work a single address can *start*, and a read-modify-write
// counter would admit a whole burst past the limit together.
//
// Every request here is malformed. The budget is spent before the handler runs, so
// a request that never reaches an authentication is still counted — which is both
// the property under test and the reason a flood costs the test nothing in CPU.

// postMalformedLogin issues one manual login whose body cannot be parsed, from
// the given client address.
//
// The forwarded header is only read because this app is configured to read it.
// Nothing about the address is believed on any other app.
func postMalformedLogin(
	t *testing.T,
	app *fiber.App,
	address string,
) loginResponse {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/login",
		strings.NewReader("this is not json"),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", address)

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
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

// floodLoginIPBudget spends one address's whole client budget and returns once it
// is exhausted.
func floodLoginIPBudget(t *testing.T, app *fiber.App, address string) {
	t.Helper()

	for attempt := range login.LoginIPRateLimitMax {
		result := postMalformedLogin(t, app, address)

		require.NotEqual(
			t,
			http.StatusTooManyRequests,
			result.status,
			"attempt %d should be within budget, got %s", attempt, result.code,
		)
	}

	result := postMalformedLogin(t, app, address)

	require.Equal(
		t,
		http.StatusTooManyRequests,
		result.status,
		"the budget should be spent after %d requests",
		login.LoginIPRateLimitMax,
	)
	require.Equal(t, "IP_RATE_LIMIT_EXCEEDED", result.code)
}

// A client's address is exhausted, and no other client's is. This is the whole
// reason the budget is per address: one person's flood must not stop everyone
// behind the same proxy, and one person's legitimate use must not be spent by
// someone else.
func TestLoginIPRateLimit_OneAddressDoesNotSpendAnother(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	const flooder = "198.51.100.70"
	const neighbour = "198.51.100.71"

	app, _ := newIPLimitedApp(t)

	floodLoginIPBudget(t, app, flooder)

	// The neighbour is untouched: same proxy, same moment, same route.
	require.NotEqual(
		t,
		http.StatusTooManyRequests,
		postMalformedLogin(t, app, neighbour).status,
		"a neighbour was blocked by another client's flood",
	)

	// And the flooded address is still refused, so nothing was quietly restored.
	require.Equal(
		t,
		http.StatusTooManyRequests,
		postMalformedLogin(t, app, flooder).status,
	)
}

// The budget is atomic across concurrent requests from one address. A client that
// fires its requests in parallel must not get more than its allowance by having
// several in flight at once.
//
// This is the property the fake limiter cannot prove, and the reason the budget is
// charged rather than checked: with only a read, every request in a burst would
// read "within budget" and every one of them would go on to pay for a derivation.
func TestLoginIPRateLimit_ConcurrentRequestsCannotExceedTheBudget(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	app, _ := newIPLimitedApp(t)

	const (
		address = "198.51.100.72"
		// Comfortably more than the budget, so an over-admission is visible
		// rather than masked by requests that would be refused anyway.
		attempts = login.LoginIPRateLimitMax * 3
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

			if postMalformedLogin(t, app, address).status ==
				http.StatusTooManyRequests {
				refused.Add(1)
			}
		}()
	}

	ready.Wait()
	close(start)
	done.Wait()

	require.Equal(
		t,
		int64(attempts-login.LoginIPRateLimitMax),
		refused.Load(),
		"a client must not exceed its budget by sending in parallel",
	)
}

// The refused response carries a wait a client can act on, and it is derived from
// the counter rather than from the configured window.
func TestLoginIPRateLimit_ExceededCarriesRetryAfter(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	app, _ := newIPLimitedApp(t)

	const address = "198.51.100.73"

	floodLoginIPBudget(t, app, address)

	var waits []int

	// A few refusals in a row. Each must report a wait, and none may extend the
	// window: a refused request spends budget the window already had.
	for range 3 {
		result := postMalformedLogin(t, app, address)

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
			int(login.LoginIPRateLimitWindow.Seconds()),
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

// A request the client budget refused never reaches the handler, so it can never
// reach the per-address budget in the service. A flood must cost a locked-out
// account nothing: what was spent was a network budget, not the account's.
//
// This is asserted with a real account, so the per-address budget is available to
// spend and its remaining allowance is observable rather than absent.
func TestLoginIPRateLimit_DoesNotSpendTheEmailBudget(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	const (
		email    = "login-ip-then-email@example.com"
		password = "correct-password"
	)

	userID := createLoginAccount(t, email, password)

	address := "198.51.100.74"

	app, _ := newIPLimitedApp(t)

	floodLoginIPBudget(t, app, address)

	// Every attempt above was a body the handler refused, so nothing reached the
	// account. The whole per-address allowance is therefore still available, and
	// the first real guess against the account — from a different address, so the
	// client budget is not what is answering — is spent, not refused.
	result := postLogin(t, app, email, "wrong-password")

	require.Equal(
		t,
		http.StatusUnauthorized,
		result.status,
		"a client budget spent the account's budget: %s", result.code,
	)
	require.Equal(t, login.CodeInvalidCredentials, result.code)

	sessionCount, err := db.CountUserSessions(ctx, userID)
	require.NoError(t, err)
	require.Zero(t, sessionCount)
}

// A header the application is not configured to believe cannot move a client's
// budget. Without this, one address's flood could be spread across a keyspace of
// invented identities, and the counter would be counting nothing at all.
//
// The app here trusts no proxy, so every request presents the same peer however
// it asks to be identified. The proxy-configured app is a different app, and one
// that is told to believe a header must believe it — see the addresses this file
// drives above, which is what that one is for.
func TestLoginIPRateLimit_SpoofedHeadersCannotBypassTheLimit(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	limiter := testutil.NewCountingPinRateLimiter()

	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	spoofed := []string{
		"198.51.100.1",
		"198.51.100.2",
		"203.0.113.7",
		"2001:db8::1",
	}

	for _, address := range spoofed {
		req := httptest.NewRequest(
			http.MethodPost,
			"/auth/login",
			strings.NewReader("this is not json"),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", address)
		req.Header.Set("X-Real-IP", address)
		req.Header.Set("CF-Connecting-IP", address)
		req.Header.Set("True-Client-IP", address)

		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	}

	// Every one of them landed on the peer's own address, so one budget was spent
	// four times rather than four budgets being spent once.
	subjects := limiter.SubjectsIn(testutil.LoginIPNamespace)
	require.Len(t, subjects, len(spoofed))

	for _, subject := range subjects {
		require.Equal(
			t,
			"0.0.0.0",
			subject,
			"a spoofed header must not move the client address",
		)
	}
}

// The budget belongs to the client address, not to the route, so a request the
// limiter refuses on one login route would consume the same allowance a caller
// has on the others. There is only one limited route today; what is pinned here
// is that the Google routes, which are unlimited by decision, do not spend it.
func TestLoginIPRateLimit_GoogleLoginIsNotCounted(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	app, _ := newIPLimitedApp(t)

	const address = "198.51.100.75"

	// An id that is not one. The limiter is mounted in front of path parsing, so
	// had the Google routes been given it, the budget would already have been
	// spent by the time the id turned out to be unusable.
	for range login.LoginIPRateLimitMax {
		req := httptest.NewRequest(
			http.MethodPost,
			"/auth/login/google/account-link/not-a-uuid",
			strings.NewReader(""),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", address)

		resp, err := app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
		require.NoError(t, err)
		require.NotEqual(
			t,
			http.StatusTooManyRequests,
			resp.StatusCode,
			"Google login was refused by a budget it is not behind",
		)
	}

	// The manual login budget is untouched, so the whole allowance is still there.
	result := postMalformedLogin(t, app, address)

	require.NotEqual(
		t,
		http.StatusTooManyRequests,
		result.status,
		"the Google routes spent the manual login budget: %s", result.code,
	)
}
