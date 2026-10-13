package integration_test

import (
	"context"
	"encoding/json"
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

// The per-client-address budgets on the verification and manual-finalization
// routes, against real Redis.
//
// The fake limiter proves the wiring; only a real counter proves that concurrent
// requests from one address cannot jointly exceed the allowance. That property is
// the whole reason these budgets are charged in front of the handler, and it is
// already proven once for pin-ip in auth_pin_ip_rate_limit_test.go. What is
// specific here is namespace separation: verify and finalize are adjacent steps
// of one registration, so each has to be shown unable to spend the other's
// allowance, and neither may touch the login budgets.
//
// Requests naming nothing real are used deliberately. Both budgets are spent
// before the handler runs, so a request carrying an unusable id, or no token at
// all, is still counted in full — which is the property under test and the reason
// a flood here costs no Argon2id.

// postVerifyFrom issues one verification attempt from the given client address.
//
// The forwarded header is only read because this app is configured to read it.
// Nothing about an address is believed on any other app, which
// TestRegisterAPI_IPRateLimit_SpoofedHeadersCannotBypassTheLimit pins.
func postVerifyFrom(
	t *testing.T,
	app *fiber.App,
	verificationID uuid.UUID,
	address string,
) pinOutcome {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/verification/"+verificationID.String()+"/verify",
		strings.NewReader(`{"pin":"000000"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", address)

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	require.NoError(t, err)

	return readIPLimitedOutcome(t, resp)
}

// postFinalizeFrom issues one finalization attempt carrying no continuation
// token, from the given client address.
//
// No token is deliberate. The budget is charged before the handler extracts one,
// so a request that would have been refused with 401 is still counted — and
// sending a real token would spend an Argon2id derivation per request, which is
// both slow and irrelevant to what is being tested.
func postFinalizeFrom(
	t *testing.T,
	app *fiber.App,
	address string,
) pinOutcome {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/finalize/manual",
		strings.NewReader(`{"display_name":"Rater","password":"password123"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", address)

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	require.NoError(t, err)

	return readIPLimitedOutcome(t, resp)
}

// readIPLimitedOutcome turns a response into the status, error code and
// Retry-After these budgets are asserted on.
func readIPLimitedOutcome(t *testing.T, resp *http.Response) pinOutcome {
	t.Helper()

	outcome := pinOutcome{
		status:     resp.StatusCode,
		retryAfter: resp.Header.Get(fiber.HeaderRetryAfter),
	}

	if outcome.status < 400 {
		return outcome
	}

	var body registerAPIError

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	outcome.code = body.Error.Code

	return outcome
}

// floodVerifyBudget spends one address's whole verification budget.
func floodVerifyBudget(t *testing.T, app *fiber.App, address string) {
	t.Helper()

	for attempt := range registration.VerifyIPRateLimitMax {
		outcome := postVerifyFrom(t, app, uuid.New(), address)

		require.NotEqual(
			t,
			http.StatusTooManyRequests,
			outcome.status,
			"attempt %d should be within budget, got %s", attempt, outcome.code,
		)
	}

	outcome := postVerifyFrom(t, app, uuid.New(), address)

	require.Equal(
		t,
		http.StatusTooManyRequests,
		outcome.status,
		"the verification budget should be spent after %d requests",
		registration.VerifyIPRateLimitMax,
	)
	require.Equal(t, "IP_RATE_LIMIT_EXCEEDED", outcome.code)
}

// floodFinalizeBudget spends one address's whole finalization budget.
func floodFinalizeBudget(t *testing.T, app *fiber.App, address string) {
	t.Helper()

	for attempt := range registration.FinalizeIPRateLimitMax {
		outcome := postFinalizeFrom(t, app, address)

		require.NotEqual(
			t,
			http.StatusTooManyRequests,
			outcome.status,
			"attempt %d should be within budget, got %s", attempt, outcome.code,
		)
	}

	outcome := postFinalizeFrom(t, app, address)

	require.Equal(
		t,
		http.StatusTooManyRequests,
		outcome.status,
		"the finalization budget should be spent after %d requests",
		registration.FinalizeIPRateLimitMax,
	)
	require.Equal(t, "IP_RATE_LIMIT_EXCEEDED", outcome.code)
}

// The verification budget is spent after exactly its own number of requests, and
// the refusal carries a wait derived from the counter rather than from the
// configured window.
func TestVerifyIPRateLimit_ExceededCarriesRetryAfter(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	app, _ := newIPLimitedApp(t)

	const address = "198.51.100.60"

	floodVerifyBudget(t, app, address)

	var waits []int

	// Refusals in a row. Each reports a wait, and none extends the window: a
	// refused request must not be able to hold the counter open.
	for range 3 {
		outcome := postVerifyFrom(t, app, uuid.New(), address)

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
			"Retry-After must be whole seconds, got %q", outcome.retryAfter,
		)
		require.Positive(t, seconds)
		require.LessOrEqual(
			t,
			seconds,
			int(registration.VerifyIPRateLimitWindow.Seconds()),
			"the wait must never exceed the window it came from",
		)

		waits = append(waits, seconds)
	}

	for index := 1; index < len(waits); index++ {
		require.LessOrEqual(
			t,
			waits[index],
			waits[index-1],
			"a refused request must not extend the window: waits %v", waits,
		)
	}
}

// The finalization budget is the tightest of the three and is spent after
// exactly its own number of requests.
func TestFinalizeIPRateLimit_ExceededReturns429(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	app, _ := newIPLimitedApp(t)

	const address = "198.51.100.61"

	floodFinalizeBudget(t, app, address)

	outcome := postFinalizeFrom(t, app, address)

	require.Equal(t, http.StatusTooManyRequests, outcome.status)
	require.Equal(t, "IP_RATE_LIMIT_EXCEEDED", outcome.code)
	require.NotEmpty(t, outcome.retryAfter)
}

// The budget belongs to the address, so one client's flood must not stop another
// behind the same proxy from reaching the same route.
func TestVerifyIPRateLimit_OneAddressDoesNotSpendAnother(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	app, _ := newIPLimitedApp(t)

	verificationID := openPendingRegistration(t, "verify-independent@example.com")

	const (
		flooder   = "198.51.100.62"
		neighbour = "198.51.100.63"
	)

	floodVerifyBudget(t, app, flooder)

	// The neighbour reaches the handler: a wrong PIN is refused by the code, not
	// by the client budget, which is what tells the two apart.
	outcome := postVerifyFrom(t, app, verificationID, neighbour)

	require.NotEqual(
		t,
		http.StatusTooManyRequests,
		outcome.status,
		"a neighbour was blocked by another client's flood: %s", outcome.code,
	)

	// And the flooded address is still refused, so nothing was quietly restored.
	require.Equal(
		t,
		http.StatusTooManyRequests,
		postVerifyFrom(t, app, uuid.New(), flooder).status,
	)
}

// Verification and finalization are adjacent steps of one registration and are
// two different acts, so exhausting one must not spend the other's allowance. A
// caller who mistyped a PIN has to be able to finalize once they get it right.
func TestRegistrationIPRateLimit_VerifyAndFinalizeAreSeparate(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	app, _ := newIPLimitedApp(t)

	const address = "198.51.100.64"

	// Spend the entire verification budget.
	floodVerifyBudget(t, app, address)

	// The finalization budget is untouched, so the first request through it is
	// still within budget and reaches the handler.
	outcome := postFinalizeFrom(t, app, address)

	require.NotEqual(
		t,
		http.StatusTooManyRequests,
		outcome.status,
		"an exhausted verification budget locked out finalization: %s",
		outcome.code,
	)

	// And the reverse: spending finalization leaves verification alone. This one
	// is worth asserting in both directions because a shared counter would pass
	// the first half and fail here.
	//
	// A second address, because the first has already spent its verification
	// budget above; reusing it would test that budget rather than the separation.
	const otherAddress = "198.51.100.69"

	for range registration.FinalizeIPRateLimitMax {
		postFinalizeFrom(t, app, otherAddress)
	}

	require.Equal(
		t,
		http.StatusTooManyRequests,
		postFinalizeFrom(t, app, otherAddress).status,
	)

	outcome = postVerifyFrom(t, app, uuid.New(), otherAddress)

	require.NotEqual(
		t,
		http.StatusTooManyRequests,
		outcome.status,
		"an exhausted finalization budget locked out verification: %s",
		outcome.code,
	)
}

// Neither new budget may touch the PIN budgets or the login budgets. Every one
// is a separate counter, and a flood of one flow must not be able to lock out
// another.
//
// This is the property that decides whether the namespaces stay distinct, so it
// is asserted from the limiter's own accounting rather than from response codes:
// a request refused by the wrong budget would still be a 429, and only the
// namespace each call landed on shows which counter was spent.
func TestRegistrationIPRateLimit_NewNamespacesDoNotSpendExistingBudgets(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	limiter := testutil.NewCountingPinRateLimiter()
	budget := testutil.NewCountingPINEmailBudget()

	app, _ := testutil.NewAppWithTrustedProxyAndBudget(testPool, limiter, budget)

	const address = "198.51.100.65"

	verificationID := openPendingRegistration(t, "namespaces@example.com")

	// One request through each of the four budgeted registration routes.
	postFrom(t, app, verificationID, address)
	postVerifyFrom(t, app, verificationID, address)
	postFinalizeFrom(t, app, address)

	// A login request from the same address.
	loginReq := httptest.NewRequest(
		http.MethodPost,
		"/auth/login",
		strings.NewReader("this is not json"),
	)
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("X-Forwarded-For", address)

	resp, err := app.Test(loginReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Each client-address budget recorded exactly one charge, in its own
	// namespace.
	require.Equal(t, 1, limiter.CallsIn(testutil.PinIPNamespace))
	require.Equal(t, 1, limiter.CallsIn(testutil.VerifyIPNamespace))
	require.Equal(t, 1, limiter.CallsIn(testutil.FinalizeIPNamespace))
	require.Equal(t, 1, limiter.CallsIn(testutil.LoginIPNamespace))

	// The PIN issuance request is the only one of these that carries a per-address
	// dimension, so it is the only one that reaches the email budget. A verify or
	// finalize request never consults it, which is what keeps a mistyped PIN from
	// spending the allowance that governs mailing a new one.
	//
	// The budget is a separate component from the limiter rather than another
	// namespace on it, so this is asserted through it directly.
	require.Equal(t, 1, budget.PerEmailCount())
	require.Equal(t, 1, budget.GlobalCount())
	require.Zero(
		t,
		limiter.CallsIn(testutil.PinEmailNamespace),
		"the per-address PIN budget is no longer spent through this limiter",
	)

	// And the budgets belonging to other flows were never touched at all.
	require.Zero(t, limiter.CallsIn(testutil.GoogleAuthIPNamespace))
	require.Zero(t, limiter.CallsIn(testutil.LoginEmailNamespace))

	// Every one of them was charged against the same address, which is what makes
	// them comparable and what a shared budget would have collapsed into one.
	for _, namespace := range []string{
		testutil.PinIPNamespace,
		testutil.VerifyIPNamespace,
		testutil.FinalizeIPNamespace,
		testutil.LoginIPNamespace,
	} {
		require.Equal(
			t,
			[]string{address},
			limiter.SubjectsIn(namespace),
			"namespace %s charged the wrong address", namespace,
		)
	}
}

// The two routes that are deliberately NOT limited must stay that way. Both are
// pure reads: limiting them would cost a legitimate polling client its budget
// without bounding any expensive work, and GetVerification in particular is what
// a client calls to learn when it may resend.
func TestRegistrationIPRateLimit_UnlimitedReadRoutesAreNotCounted(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	limiter := testutil.NewCountingPinRateLimiter()

	app, _ := testutil.NewAppWithTrustedProxy(testPool, limiter)

	const address = "198.51.100.66"

	verificationID := openPendingRegistration(t, "unlimited-reads@example.com")

	// GetVerification, repeatedly. It is what a client polls while waiting out a
	// resend cooldown, so it must never be the thing that refuses it.
	for range registration.VerifyIPRateLimitMax + 2 {
		req := httptest.NewRequest(
			http.MethodGet,
			"/auth/register/verification/"+verificationID.String(),
			nil,
		)
		req.Header.Set("X-Forwarded-For", address)

		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}

	// GetRegistrationContinuation, repeatedly, with a token that resolves to
	// nothing. It reports the same conflict every time and is still free.
	for range registration.VerifyIPRateLimitMax + 2 {
		req := httptest.NewRequest(
			http.MethodGet,
			"/auth/register/continuation",
			nil,
		)
		req.Header.Set("X-Forwarded-For", address)
		req.Header.Set("X-Registration-Continuation", "not-a-real-token")

		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusConflict, resp.StatusCode)
	}

	// Neither read route consulted any budget at all.
	require.Zero(t, limiter.CallsIn(testutil.VerifyIPNamespace))
	require.Zero(t, limiter.CallsIn(testutil.FinalizeIPNamespace))
	require.Empty(t, limiter.Subjects)
}

// A limiter that cannot answer refuses rather than admits. The two new routes
// inherit this from the shared middleware, and it has to hold for them: a Redis
// outage that let finalization through would turn the one Argon2id route in the
// flow into an unbounded one exactly when the process is already unhealthy.
//
// The refusal is a 503 carrying no Retry-After, because there is no known
// recovery time and naming one would be a guess presented as a fact.
func TestRegistrationIPRateLimit_UnavailableRefusesWith503(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	verificationID := openPendingRegistration(t, "unavailable@example.com")

	limiter := testutil.UnavailablePinRateLimiter(
		context.DeadlineExceeded,
	)

	app, _ := testutil.NewAppWithTrustedProxy(testPool, limiter)

	const address = "198.51.100.67"

	for name, call := range map[string]func() pinOutcome{
		"verify": func() pinOutcome {
			return postVerifyFrom(t, app, verificationID, address)
		},
		"finalize": func() pinOutcome {
			return postFinalizeFrom(t, app, address)
		},
	} {
		t.Run(name, func(t *testing.T) {
			outcome := call()

			require.Equal(t, http.StatusServiceUnavailable, outcome.status)
			require.Equal(t, "IP_RATE_LIMIT_UNAVAILABLE", outcome.code)

			require.Empty(
				t,
				outcome.retryAfter,
				"a 503 must not name a recovery time nobody chose",
			)
		})
	}
}

// The budget is charged before path parsing, so a request naming no verification
// is counted too. A limit that only counted well-formed requests would be free
// to burn with the cheapest request there is, and that request is one whose id
// is not a UUID.
//
// This is the placement property, asserted at the boundary: the request still
// gets a 400 from the handler, which proves the handler ran, while the budget was
// nonetheless charged.
func TestVerifyIPRateLimit_MalformedIDIsStillCounted(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	limiter := testutil.NewCountingPinRateLimiter()

	app, _ := testutil.NewAppWithTrustedProxy(testPool, limiter)

	const address = "198.51.100.68"

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/verification/not-a-uuid/verify",
		strings.NewReader(`{"pin":"000000"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", address)

	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	require.Equal(t, 1, limiter.CallsIn(testutil.VerifyIPNamespace))
	require.Equal(t, []string{address}, limiter.SubjectsIn(testutil.VerifyIPNamespace))
}
