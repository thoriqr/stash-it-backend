package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	registration "github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	registrationtestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/registration/generated"
)

// These tests run the IP budget through the real Redis and through HTTP, because
// neither half proves the other. A fake proves the wiring, and a limiter-level
// test proves the counter; only running both together shows that the address the
// application actually resolves is what decides which counter a request spends.
//
// The app here is built as if behind a verified proxy, because `app.Test` serves
// every request from an in-memory connection whose only address is the
// unspecified one. That is the only way to drive distinct client addresses
// through the real routing without a real socket to vary, and it exercises the
// same trusted-proxy path a deployment would use.
//
// Most cases here spend the client budget against verification IDs that do not
// exist. That is deliberate rather than a shortcut: the client budget is spent in
// front of the handler, so those requests are counted in full, and nothing is
// issued for them. Reaching for the client budget through a real verification
// instead would run into the per-email budget — 5 an hour against 20 in ten
// minutes — and every assertion would be about whichever budget was smaller.

const ipRateLimitTestSecret = "ip-rate-limit-integration-secret"

// newIPLimitedApp builds the app with the real Redis-backed limiter, clearing the
// limiter's keys first so one test cannot see another's counts.
func newIPLimitedApp(t *testing.T) (*fiber.App, *testutil.FakeEmailSender) {
	t.Helper()

	client, err := testutil.StartRedisForTests(t.Context())
	require.NoError(t, err)

	resetRateLimitKeys(t, client)

	return testutil.NewAppWithTrustedProxy(
		testPool,
		ratelimit.New(client, []byte(ipRateLimitTestSecret)),
	)
}

// resetRateLimitKeys removes every limiter counter.
//
// The integration suite shares one Redis and one database and tests must not
// collide on either. Only the limiter's own namespace is cleared, so the queue's
// keys and anything else a test is relying on are untouched.
func resetRateLimitKeys(t *testing.T, client redis.UniversalClient) {
	t.Helper()

	ctx := t.Context()

	keys, err := client.Keys(ctx, "rl:*").Result()
	require.NoError(t, err)

	if len(keys) > 0 {
		require.NoError(t, client.Del(ctx, keys...).Err())
	}
}

type pinOutcome struct {
	status     int
	code       string
	retryAfter string
}

// postFrom issues one request to the PIN route presenting itself as coming from
// the given address.
//
// The forwarded header is only read because this app is configured to read it.
// Nothing about the address is believed on any other app, which
// TestRegisterAPI_IPRateLimit_SpoofedHeadersCannotBypassTheLimit pins.
func postFrom(
	t *testing.T,
	app *fiber.App,
	verificationID uuid.UUID,
	address string,
) pinOutcome {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/verification/"+verificationID.String()+pinPath,
		nil,
	)
	req.Header.Set("X-Forwarded-For", address)

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	require.NoError(t, err)

	outcome := pinOutcome{
		status:     resp.StatusCode,
		retryAfter: resp.Header.Get(fiber.HeaderRetryAfter),
	}

	if outcome.status < 400 {
		return outcome
	}

	var body registerAPIError

	require.NoError(
		t,
		json.NewDecoder(resp.Body).Decode(&body),
	)

	outcome.code = body.Error.Code

	return outcome
}

// floodIPBudget spends one address's whole client budget and returns once it is
// exhausted.
//
// Every request names a verification that does not exist, so the per-email budget
// is never involved and the only thing that can refuse these is the client budget.
func floodIPBudget(t *testing.T, app *fiber.App, address string) {
	t.Helper()

	for attempt := range registration.PinIPRateLimitMax {
		outcome := postFrom(
			t,
			app,
			// A well-formed id that resolves to nothing. The request is counted
			// before the handler ever looks at it.
			uuid.New(),
			address,
		)

		require.NotEqual(
			t,
			http.StatusTooManyRequests,
			outcome.status,
			"attempt %d should be within budget", attempt,
		)
	}

	outcome := postFrom(t, app, uuid.New(), address)

	require.Equal(
		t,
		http.StatusTooManyRequests,
		outcome.status,
		"the budget should be spent after %d requests",
		registration.PinIPRateLimitMax,
	)
	require.Equal(t, "IP_RATE_LIMIT_EXCEEDED", outcome.code)
}

// A client's address is exhausted, and no other client's is. This is the whole
// reason the budget is per address: one person's flood must not stop everyone
// behind the same proxy, and one person's legitimate use must not be spent by
// someone else.
func TestPinIPRateLimit_OneAddressDoesNotSpendAnother(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	app, emailSender := newIPLimitedApp(t)

	verificationID := openPendingRegistration(
		t,
		"ip-independent@example.com",
	)

	const flooder = "198.51.100.10"
	const neighbour = "198.51.100.11"

	floodIPBudget(t, app, flooder)

	// The neighbour is untouched: same proxy, same verification, same moment. The
	// verification has not been issued for at all, so nothing about the account
	// was spent either.
	outcome := postFrom(t, app, verificationID, neighbour)

	require.Equal(
		t,
		http.StatusCreated,
		outcome.status,
		"a neighbour at %s was blocked by another client's flood: %s",
		neighbour,
		outcome.code,
	)

	require.Len(t, emailSender.Messages, 1)

	// And the flooded address is still refused, so nothing was quietly restored.
	require.Equal(
		t,
		http.StatusTooManyRequests,
		postFrom(t, app, uuid.New(), flooder).status,
	)
}

// The refused response carries a wait a client can act on, and it is derived from
// the counter rather than from the configured window.
func TestPinIPRateLimit_ExceededCarriesRetryAfter(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	app, _ := newIPLimitedApp(t)

	const address = "198.51.100.20"

	floodIPBudget(t, app, address)

	var waits []int

	// A few refusals in a row. Each must report a wait, and none may extend the
	// window: a refused request spends budget the window already had.
	for range 3 {
		outcome := postFrom(t, app, uuid.New(), address)

		require.Equal(t, http.StatusTooManyRequests, outcome.status)
		require.NotEmpty(
			t,
			outcome.retryAfter,
			"a 429 must tell the client when to come back",
		)

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
			int(registration.PinIPRateLimitWindow.Seconds()),
			"the wait must never exceed the window it came from",
		)

		waits = append(waits, seconds)
	}

	// The window is not pushed forward by the requests being refused, so the
	// reported wait can only hold steady or fall. A wait that climbed would mean
	// a refused caller could hold the counter open indefinitely.
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
func TestPinIPRateLimit_ConcurrentRequestsCannotExceedTheBudget(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	app, _ := newIPLimitedApp(t)

	const (
		address = "198.51.100.30"
		// Comfortably more than the budget, so an over-admission is visible
		// rather than masked by requests that would be refused anyway.
		attempts = registration.PinIPRateLimitMax * 3
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

			outcome := postFrom(t, app, uuid.New(), address)

			if outcome.status == http.StatusTooManyRequests {
				refused.Add(1)
			}
		}()
	}

	ready.Wait()
	close(start)
	done.Wait()

	require.Equal(
		t,
		int64(attempts-registration.PinIPRateLimitMax),
		refused.Load(),
		"a client must not exceed its budget by sending in parallel",
	)
}

// One address exhausting its client budget must not lock another address out of
// the same verification. What was spent was a network budget, not the account's.
func TestPinIPRateLimit_VerificationIsNotLockedByAnotherAddress(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	app, emailSender := newIPLimitedApp(t)

	verificationID := openPendingRegistration(
		t,
		"ip-shared-verification@example.com",
	)

	floodIPBudget(t, app, "198.51.100.50")

	require.Empty(
		t,
		emailSender.Messages,
		"nothing was issued while the budget was being spent",
	)

	// Another address, same verification. The email budget was never touched, so
	// this succeeds.
	outcome := postFrom(t, app, verificationID, "198.51.100.51")

	require.Equal(
		t,
		http.StatusCreated,
		outcome.status,
		"one address's client budget locked another out: %s", outcome.code,
	)

	require.Len(t, emailSender.Messages, 1)
}

// The client budget and the email budget are different counters with different
// sizes, so the smaller one is what runs out first for a single address. Both
// refusing is the proof they are separate: exhausting one does not silently
// satisfy or pre-empt the other.
func TestPinIPRateLimit_EmailBudgetIsSeparate(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	app, emailSender := newIPLimitedApp(t)

	verificationID := openPendingRegistration(
		t,
		"ip-and-email@example.com",
	)

	const address = "198.51.100.40"

	// The email budget allows 5 an hour and the client budget allows 20 in ten
	// minutes, so six requests with the cooldown aged between them is past the
	// email budget and well within the client one. The sixth must be refused for
	// saying so, which is only observable if the two counters are separate.
	for range registration.PinRateLimitMax {
		outcome := postFrom(t, app, verificationID, address)
		require.Equal(t, http.StatusCreated, outcome.status, outcome.code)

		require.NoError(
			t,
			db.MakeVerificationResendable(ctx, verificationID),
		)
	}

	outcome := postFrom(t, app, verificationID, address)

	require.Equal(
		t,
		http.StatusTooManyRequests,
		outcome.status,
		"the email budget should be spent by now, got %s", outcome.code,
	)
	require.Equal(t, "PIN_RATE_LIMIT_EXCEEDED", outcome.code)

	// Six attempts against a budget of five, so exactly five were issued.
	require.Len(t, emailSender.Messages, registration.PinRateLimitMax)

	// The client budget was spent too, but nowhere near its own limit: it counted
	// all six and refused none. If the two were one counter, the sixth would have
	// said IP_RATE_LIMIT_EXCEEDED instead.
	require.NotEqual(t, "IP_RATE_LIMIT_EXCEEDED", outcome.code)
}
