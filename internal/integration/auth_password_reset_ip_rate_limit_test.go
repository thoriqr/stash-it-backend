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

	"github.com/thoriqr/stash-it-backend/internal/api/auth"
	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	passwordresetdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/password_reset/generated"
)

// These tests drive the four budgeted password reset routes through HTTP with a
// limiter the test controls, so the whole path is exercised: route, middleware,
// handler, service, limiter, and the message that would have been sent.
//
// Nothing here needs Redis. The limiter's own Redis binding is proven by the
// limiter's own integration tests; what these pin is which namespace each route
// spends, that the two issuance routes share one, that the finalize budget is
// enforced ahead of the handler, and that a refusal costs nothing.

// resetOutcome is one response, reduced to what a rate-limit assertion needs.
type resetOutcome struct {
	status     int
	code       string
	retryAfter string
}

// resetApp builds the app behind a trusted proxy so a test can drive distinct
// client addresses through it.
//
// `app.Test` serves every request from an in-memory connection whose only address
// is the unspecified one, so the forwarded header is the only way to vary the
// client. The address is read only because this app is configured to read it;
// nothing about a header is believed on any other app.
func resetApp(
	t *testing.T,
	limiter auth.RateLimiter,
) (*fiber.App, *testutil.FakeEmailSender) {
	t.Helper()

	return testutil.NewAppWithTrustedProxy(testPool, limiter)
}

// resetAppWithBudget builds the app with a caller-supplied PIN email budget as
// well as a caller-supplied limiter.
//
// It exists because this feature now spends the same per-address budget as
// registration, so a test about that budget has to be able to see what was spent
// rather than running against a budget that admits everything.
func resetAppWithBudget(
	t *testing.T,
	limiter auth.RateLimiter,
	budget auth.PINEmailBudget,
) (*fiber.App, *testutil.FakeEmailSender) {
	t.Helper()

	return testutil.NewAppWithTrustedProxyAndBudget(testPool, limiter, budget)
}

// postResetFrom opens a password reset from the given client address.
func postResetFrom(
	t *testing.T,
	app *fiber.App,
	emailAddress string,
	address string,
) resetOutcome {
	t.Helper()

	return resetCall(
		t,
		app,
		http.MethodPost,
		"/auth/password-reset",
		`{"email":"`+emailAddress+`"}`,
		address,
	)
}

// getVerificationFrom reads a verification's status from the given client address.
func getVerificationFrom(
	t *testing.T,
	app *fiber.App,
	verificationID uuid.UUID,
	address string,
) resetOutcome {
	t.Helper()

	return resetCall(
		t,
		app,
		http.MethodGet,
		"/auth/password-reset/verification/"+verificationID.String(),
		"",
		address,
	)
}

// postIssuanceFrom calls one of the two issuance routes from the given client
// address.
func postIssuanceFrom(
	t *testing.T,
	app *fiber.App,
	verificationID uuid.UUID,
	suffix string,
	address string,
) resetOutcome {
	t.Helper()

	return resetCall(
		t,
		app,
		http.MethodPost,
		"/auth/password-reset/verification/"+verificationID.String()+suffix,
		"",
		address,
	)
}

// postPinVerifyFrom calls the deliberately unlimited verification route.
func postPinVerifyFrom(
	t *testing.T,
	app *fiber.App,
	verificationID uuid.UUID,
	address string,
) resetOutcome {
	t.Helper()

	return resetCall(
		t,
		app,
		http.MethodPost,
		"/auth/password-reset/verification/"+verificationID.String()+"/verify",
		`{"pin":"123456"}`,
		address,
	)
}

func resetCall(
	t *testing.T,
	app *fiber.App,
	method string,
	path string,
	body string,
	address string,
) resetOutcome {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	req.Header.Set("X-Forwarded-For", address)

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	require.NoError(t, err)

	outcome := resetOutcome{
		status:     resp.StatusCode,
		retryAfter: resp.Header.Get(fiber.HeaderRetryAfter),
	}

	if outcome.status < 400 {
		return outcome
	}

	var decoded registerAPIError

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&decoded))

	outcome.code = decoded.Error.Code

	return outcome
}

// openPendingReset creates a user and returns the verification id its reset was
// opened under, which is what the issuance and status routes are addressed by.
func openPendingReset(t *testing.T, emailAddress string) uuid.UUID {
	t.Helper()

	db := passwordresetdbtest.New(testPool)

	_, err := db.CreatePasswordResetUser(context.Background(), emailAddress)
	require.NoError(t, err)

	// Opened through the suite's own app, whose limiter is permissive, so
	// setting up a fixture never spends the budget under test.
	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(`{"email":"`+emailAddress+`"}`),
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

// Each budgeted route spends its own namespace and its own policy, and nothing
// else.
//
// This is asserted from the limiter's own accounting rather than from response
// codes, because a request refused by the wrong budget would still be a 429 and
// only the namespace each call landed on shows which counter was spent.
func TestPasswordResetIPRateLimit_EachRouteSpendsItsOwnBudget(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	limiter := testutil.NewCountingPinRateLimiter()
	app, _ := resetApp(t, limiter)

	const address = "198.51.100.80"

	verificationID := openPendingReset(t, "budgets@example.com")

	require.Equal(
		t,
		http.StatusCreated,
		postResetFrom(t, app, "budgets@example.com", address).status,
	)

	require.Equal(
		t,
		http.StatusOK,
		getVerificationFrom(t, app, verificationID, address).status,
	)

	require.Equal(
		t,
		http.StatusCreated,
		postIssuanceFrom(t, app, verificationID, "/pin", address).status,
	)

	// One charge each, in three separate namespaces.
	require.Equal(t, 1, limiter.CallsIn(testutil.ResetRequestIPNamespace))
	require.Equal(t, 1, limiter.CallsIn(testutil.ResetStatusIPNamespace))
	require.Equal(t, 1, limiter.CallsIn(testutil.ResetPinIPNamespace))

	// Every one was charged against the same address, which is what makes them
	// comparable and what a shared budget would have collapsed into one.
	for _, namespace := range []string{
		testutil.ResetRequestIPNamespace,
		testutil.ResetStatusIPNamespace,
		testutil.ResetPinIPNamespace,
	} {
		require.Equal(
			t,
			[]string{address},
			limiter.SubjectsIn(namespace),
			"namespace %s charged the wrong address", namespace,
		)
	}

	// The policies are the ones this feature declares, not the registration
	// ones: the three acts cost different things and are bounded differently.
	policies := limiter.Policies
	require.Len(t, policies, 3)

	require.Equal(
		t,
		ratelimit.Policy{
			Max:    passwordreset.RequestIPRateLimitMax,
			Window: passwordreset.RequestIPRateLimitWindow,
		},
		policies[0],
	)

	require.Equal(
		t,
		ratelimit.Policy{
			Max:    passwordreset.StatusIPRateLimitMax,
			Window: passwordreset.StatusIPRateLimitWindow,
		},
		policies[1],
	)

	require.Equal(
		t,
		ratelimit.Policy{
			Max:    passwordreset.PinIPRateLimitMax,
			Window: passwordreset.PinIPRateLimitWindow,
		},
		policies[2],
	)

	// No other flow's budget was touched.
	require.Zero(t, limiter.CallsIn(testutil.PinIPNamespace))
	require.Zero(t, limiter.CallsIn(testutil.VerifyIPNamespace))
	require.Zero(t, limiter.CallsIn(testutil.FinalizeIPNamespace))
	require.Zero(t, limiter.CallsIn(testutil.ResetFinalizeIPNamespace))
	require.Zero(t, limiter.CallsIn(testutil.LoginIPNamespace))
	require.Zero(t, limiter.CallsIn(testutil.GoogleAuthIPNamespace))
}

// The two issuance routes draw on one budget.
//
// This is the property the shared namespace exists for. A caller that is refused
// on /pin must not be handed a fresh allowance by moving to /resend, because
// switching costs it nothing — neither route requires authentication — and
// because both reach the same issuance helper and both send a message.
func TestPasswordResetIPRateLimit_PinAndResendShareOneBudget(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	limiter := testutil.NewEnforcingPinRateLimiter()

	// The trusted-proxy app, not the peer one: this test is about two addresses
	// reaching one budget, and peer mode resolves every in-memory request to the
	// same address and would collapse them into one subject.
	app, emailSender := resetApp(t, limiter)

	const address = "198.51.100.81"

	verificationID := openPendingReset(t, "shared-budget@example.com")

	// Spend the whole budget by alternating between the two routes, so a budget
	// that was in fact two would leave both of them within allowance here.
	for attempt := range passwordreset.PinIPRateLimitMax {
		suffix := "/pin"
		if attempt%2 == 1 {
			suffix = "/resend"
		}

		// The cooldown refuses a second issuance well before the budget runs out,
		// which would make every assertion about whichever limit was smaller. The
		// verification is aged back between attempts so the cooldown is never what
		// is answering.
		require.NoError(
			t,
			db.MakeVerificationResendable(ctx, verificationID),
		)

		outcome := postIssuanceFrom(t, app, verificationID, suffix, address)

		require.NotEqualf(
			t,
			http.StatusTooManyRequests,
			outcome.status,
			"attempt %d should be within budget, got %s", attempt, outcome.code,
		)
	}

	require.Len(t, emailSender.Messages, passwordreset.PinIPRateLimitMax)

	// The budget is now spent. Whichever route the caller moves to, it is refused
	// by the same counter.
	for _, suffix := range []string{"/pin", "/resend"} {
		outcome := postIssuanceFrom(t, app, verificationID, suffix, address)

		require.Equalf(
			t,
			http.StatusTooManyRequests,
			outcome.status,
			"moving to %s was not refused by the shared budget: %s",
			suffix,
			outcome.code,
		)

		require.Equal(t, middleware.CodeIPRateLimitExceeded, outcome.code)
	}

	// No further message went out while the budget was being enforced.
	require.Len(t, emailSender.Messages, passwordreset.PinIPRateLimitMax)
}

// A refusal carries a wait the client can act on, and it is derived from the
// counter rather than from the configured window.
func TestPasswordResetIPRateLimit_ExceededCarriesRetryAfter(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	// The real limiter, because the wait is produced by the counter's own
	// remaining TTL. A fake can be told what to report, which would only prove
	// the fake reports what it was told.
	app, _ := newIPLimitedApp(t)

	const address = "198.51.100.82"

	openPendingReset(t, "retry-after@example.com")

	var waits []int

	for range passwordreset.RequestIPRateLimitMax + 3 {
		outcome := postResetFrom(
			t,
			app,
			"retry-after@example.com",
			address,
		)

		if outcome.status == http.StatusCreated {
			continue
		}

		require.Equal(t, http.StatusTooManyRequests, outcome.status)
		require.NotEmpty(
			t,
			outcome.retryAfter,
			"a 429 must tell the client when to come back",
		)

		seconds, err := strconv.Atoi(outcome.retryAfter)
		require.NoError(t, err)
		require.Positive(t, seconds)
		require.LessOrEqual(
			t,
			seconds,
			int(passwordreset.RequestIPRateLimitWindow.Seconds()),
			"the wait must never exceed the window it came from",
		)

		waits = append(waits, seconds)

	}

	require.NotEmpty(t, waits, "the budget was never spent")

	// A refused request does not push the window forward, so the reported wait can
	// only hold steady or fall.
	for index := 1; index < len(waits); index++ {
		require.LessOrEqualf(
			t,
			waits[index],
			waits[index-1],
			"a refused request must not extend the window: waits %v", waits,
		)
	}
}

// A refused request never reaches the handler, so it mutates nothing.
//
// The address below has no account, so a request that did reach the handler would
// create no state of its own — which is exactly why this uses a real account and
// asserts on the row count instead.
func TestPasswordResetIPRateLimit_RefusalMutatesNothing(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	limiter := testutil.NewEnforcingPinRateLimiter()
	app, emailSender := resetApp(t, limiter)

	const address = "198.51.100.83"
	const emailAddress = "refusal-mutates-nothing@example.com"

	// Spend the request budget on an address nobody holds first, so the account
	// below is untouched by it.
	for range passwordreset.RequestIPRateLimitMax {
		postResetFrom(t, app, "nobody-here@example.com", address)
	}

	// Now the real account, from an address whose budget is untouched.
	const other = "198.51.100.84"

	verificationID := openPendingReset(t, emailAddress)

	require.Equal(
		t,
		http.StatusCreated,
		postResetFrom(t, app, emailAddress, other).status,
	)

	state, err := db.CountPasswordResetStateForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Equal(t, int64(1), state.PendingResetCount)
	require.Equal(t, int64(1), state.VerificationRequestCount)

	// Spend the issuance budget against this verification from `address`. These
	// requests are within budget and do issue, so each one ages the cooldown back
	// rather than letting the cooldown answer instead of the budget.
	for range passwordreset.PinIPRateLimitMax {
		require.NoError(
			t,
			db.MakeVerificationResendable(ctx, verificationID),
		)

		outcome := postIssuanceFrom(t, app, verificationID, "/pin", address)
		require.NotEqual(t, http.StatusTooManyRequests, outcome.status)
	}

	require.Len(t, emailSender.Messages, passwordreset.PinIPRateLimitMax)

	// The budget is now spent. Everything below is refused in front of the
	// handler, so no further code is written, last_sent_at does not move, and
	// nothing further is sent.
	for range 3 {
		require.Equal(
			t,
			http.StatusTooManyRequests,
			postIssuanceFrom(t, app, verificationID, "/pin", address).status,
		)

		require.Equal(
			t,
			http.StatusTooManyRequests,
			postIssuanceFrom(t, app, verificationID, "/resend", address).status,
		)
	}

	require.Len(t, emailSender.Messages, passwordreset.PinIPRateLimitMax)

	state, err = db.CountPasswordResetStateForEmail(ctx, emailAddress)
	require.NoError(t, err)

	require.Equal(t, int64(1), state.PendingResetCount)
	require.Equal(t, int64(1), state.VerificationRequestCount)

	// One code per issuance that was within budget, and no more. The refusals
	// wrote nothing, which is the property: they did not invalidate the last code
	// either, so the count is unchanged rather than reset.
	require.Equal(
		t,
		int64(passwordreset.PinIPRateLimitMax),
		state.VerificationCodeCount,
		"a refused request wrote a verification code",
	)

	// The other address still issues, which is what shows the refusal was this
	// address's budget and not something global.
	require.NoError(t, db.MakeVerificationResendable(ctx, verificationID))

	outcome := postIssuanceFrom(t, app, verificationID, "/pin", other)
	require.Equal(t, http.StatusCreated, outcome.status)
	require.Len(
		t,
		emailSender.Messages,
		passwordreset.PinIPRateLimitMax+1,
	)
}

// A limiter that cannot answer refuses the request.
//
// Failing open would hand anyone the ability to switch the ceiling off by causing
// the failure that produces it, so an outage stops these routes rather than
// letting them through unthrottled. This widens the blast radius of a Redis
// outage, and the widening is deliberate.
func TestPasswordResetIPRateLimit_RedisFailureFailsClosed(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	limiter := testutil.UnavailablePinRateLimiter(
		errors.New("redis unreachable"),
	)

	app, emailSender := resetApp(t, limiter)

	const address = "198.51.100.85"

	verificationID := openPendingReset(t, "redis-down@example.com")

	requests := map[string]resetOutcome{
		"POST /auth/password-reset": postResetFrom(
			t,
			app,
			"redis-down@example.com",
			address,
		),
		"GET verification": getVerificationFrom(
			t,
			app,
			verificationID,
			address,
		),
		"POST /pin": postIssuanceFrom(
			t,
			app,
			verificationID,
			"/pin",
			address,
		),
		"POST /resend": postIssuanceFrom(
			t,
			app,
			verificationID,
			"/resend",
			address,
		),
	}

	for route, outcome := range requests {
		require.Equalf(
			t,
			http.StatusServiceUnavailable,
			outcome.status,
			"%s must fail closed when the limiter cannot answer: %s",
			route,
			outcome.code,
		)

		require.Equalf(
			t,
			middleware.CodeIPRateLimitUnavailable,
			outcome.code,
			"%s reported the wrong failure",
			route,
		)

		// A 503 from an unreachable counter carries no wait: the limiter has no
		// window to name, and inventing one would tell the client to come back at
		// a moment nobody chose.
		require.Emptyf(
			t,
			outcome.retryAfter,
			"%s must not fabricate a Retry-After",
			route,
		)
	}

	// Nothing was issued while the limiter was unreachable.
	require.Empty(t, emailSender.Messages)

	state, err := db.CountPasswordResetStateForEmail(ctx, "redis-down@example.com")
	require.NoError(t, err)
	require.Equal(t, int64(1), state.PendingResetCount)
	require.Zero(t, state.VerificationCodeCount)
}

// The one route that is not budgeted stays that way.
//
// The continuation lookup is a pure read — one indexed lookup that writes
// nothing — and a client watching its own reset while waiting out the sixty
// second cooldown would spend a budget without bounding any work. It is mounted
// with no middleware, so no budget is consulted for it, and this test exists to
// stop a later change from quietly giving it one.
func TestPasswordResetIPRateLimit_UnlimitedReadRouteSpendsNoBudget(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	limiter := testutil.NewCountingPinRateLimiter()
	budget := testutil.NewCountingPINEmailBudget()
	app, _ := resetAppWithBudget(t, limiter, budget)

	const address = "198.51.100.86"

	for range passwordreset.RequestIPRateLimitMax + 5 {
		continuation := resetCall(
			t,
			app,
			http.MethodGet,
			"/auth/password-reset/continuation",
			"",
			address,
		)

		// 401 rather than 409: the header is what this route authenticates with,
		// and an absent one is refused before a token is ever looked up. It is
		// still a pure refusal that consults no budget, which is the point.
		require.Equal(t, http.StatusUnauthorized, continuation.status)
	}

	// Not one budget was consulted, so no number of calls to this route could
	// ever exhaust one.
	require.Zero(t, limiter.CallsIn(testutil.ResetRequestIPNamespace))
	require.Zero(t, limiter.CallsIn(testutil.ResetStatusIPNamespace))
	require.Zero(t, limiter.CallsIn(testutil.ResetPinIPNamespace))
	require.Zero(t, limiter.CallsIn(testutil.ResetVerifyIPNamespace))
	require.Zero(t, limiter.CallsIn(testutil.ResetFinalizeIPNamespace))

	// The budgeted routes were not called here either.
	require.Zero(t, limiter.CallsIn(testutil.LoginIPNamespace))
	require.Zero(t, budget.PerEmailCount())
	require.Zero(t, budget.GlobalCount())
}

// An unusable limiter refuses verification rather than letting it through.
//
// Failing open would hand anyone the ability to switch the ceiling off by causing
// the failure that produces it. This widens the blast radius of a Redis outage,
// and the widening is deliberate: a request that reached the handler would spend
// a code attempt, which is a resource the five-attempt budget exists to protect.
func TestPasswordResetIPRateLimit_VerifyRedisFailureFailsClosed(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	limiter := testutil.UnavailablePinRateLimiter(
		errors.New("redis unreachable"),
	)

	app, _ := resetApp(t, limiter)

	outcome := postPinVerifyFrom(
		t,
		app,
		uuid.New(),
		"198.51.100.95",
	)

	require.Equal(t, http.StatusServiceUnavailable, outcome.status)
	require.Equal(t, middleware.CodeIPRateLimitUnavailable, outcome.code)

	// No fabricated wait: the counter has no window to name, and inventing one
	// would tell the client to come back at a moment nobody chose.
	require.Empty(t, outcome.retryAfter)
}

// The verification budget is spent after exactly its own number of requests, and
// the refusal carries a wait derived from the counter rather than from the
// configured window.
//
// The real limiter is used, because the wait is produced by the counter's own
// remaining TTL. A fake can be told what to report, which would only prove the
// fake reports what it was told.
func TestPasswordResetIPRateLimit_VerifyExceededCarriesRetryAfter(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	app, _ := newIPLimitedApp(t)

	const address = "198.51.100.96"

	// A verification id that resolves to nothing keeps every request a pure
	// refusal: the budget is charged in front of the handler, so a request that
	// would have been answered 404 is still counted, and nothing is consumed.
	var waits []int

	for range passwordreset.VerifyIPRateLimitMax + 3 {
		outcome := postPinVerifyFrom(t, app, uuid.New(), address)

		if outcome.status == http.StatusNotFound {
			continue
		}

		require.Equal(t, http.StatusTooManyRequests, outcome.status)
		require.Equal(t, middleware.CodeIPRateLimitExceeded, outcome.code)

		require.NotEmpty(
			t,
			outcome.retryAfter,
			"a 429 must tell the client when to come back",
		)

		seconds, err := strconv.Atoi(outcome.retryAfter)
		require.NoError(t, err)
		require.Positive(t, seconds)
		require.LessOrEqual(
			t,
			seconds,
			int(passwordreset.VerifyIPRateLimitWindow.Seconds()),
			"the wait must never exceed the window it came from",
		)

		waits = append(waits, seconds)
	}

	require.Len(
		t,
		waits,
		3,
		"the budget should be spent after exactly its own number of requests",
	)

	// A refused request does not push the window forward, so the reported wait can
	// only hold steady or fall.
	for index := 1; index < len(waits); index++ {
		require.LessOrEqualf(
			t,
			waits[index],
			waits[index-1],
			"a refused request must not extend the window: waits %v",
			waits,
		)
	}
}

// The verification budget is charged before the handler, so a refused request
// spends no code attempt and changes no verification state.
//
// This is the property that makes the budget worth having. The five-attempt limit
// belongs to the code row, and a refused request must not eat into it: a caller
// behind a shared address would otherwise be unable to verify at all, having had
// their budget spent by requests it never got to make.
func TestPasswordResetIPRateLimit_VerifyRefusalSpendsNoCodeAttempt(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "verify-budget@example.com"

	verificationID := openPendingReset(t, emailAddress)

	// Issue a code and pin its hash to a PIN the tests below never submit.
	// postPinVerifyFrom always submits "123456", so hashing "654321" makes every
	// request a genuine wrong guess — which is what spends an attempt. Pinning the
	// code to "123456" instead would make the first request succeed and consume it.
	require.Equal(
		t,
		http.StatusCreated,
		resetCall(
			t,
			testApp,
			http.MethodPost,
			"/auth/password-reset/verification/"+verificationID.String()+"/pin",
			"",
			"",
		).status,
	)

	require.NoError(
		t,
		db.SetVerificationCodeHash(
			ctx,
			passwordresetdbtest.SetVerificationCodeHashParams{
				CodeHash: security.NewVerificationCodeHasher(
					[]byte("integration-test-verification-secret"),
				).Hash("654321"),
				VerificationRequestID: verificationID,
			},
		),
	)

	attemptsBefore, err := countVerificationCodeAttempts(t, verificationID)
	require.NoError(t, err)
	require.Equal(
		t,
		int32(0),
		attemptsBefore,
		"a freshly issued code starts at zero",
	)

	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := resetApp(t, limiter)

	const address = "198.51.100.97"

	// Far more wrong guesses than one code can spend. The per-code budget is
	// enforced by the database and is independent of this IP budget, so the
	// counter stops at five no matter how many requests arrive.
	for attempt := range passwordreset.VerifyIPRateLimitMax {
		outcome := postPinVerifyFrom(t, app, verificationID, address)

		require.NotEqualf(
			t,
			http.StatusTooManyRequests,
			outcome.status,
			"attempt %d should be within budget, got %s",
			attempt,
			outcome.code,
		)
	}

	attemptsSpent, err := countVerificationCodeAttempts(t, verificationID)
	require.NoError(t, err)

	require.Equal(
		t,
		int32(passwordreset.VerificationCodeMaxAttempts),
		attemptsSpent,
		"the per-code limit caps attempts regardless of how many requests arrive",
	)

	require.Less(
		t,
		int(attemptsSpent),
		passwordreset.VerifyIPRateLimitMax,
		"the IP budget and the per-code limit are separate ceilings",
	)

	// Now the IP budget is spent. These are refused in front of the handler, so
	// they must not spend anything further.
	for range 3 {
		refused := postPinVerifyFrom(t, app, verificationID, address)

		require.Equal(t, http.StatusTooManyRequests, refused.status)
		require.Equal(t, middleware.CodeIPRateLimitExceeded, refused.code)
	}

	attemptsAfter, err := countVerificationCodeAttempts(t, verificationID)
	require.NoError(t, err)

	require.Equal(
		t,
		attemptsSpent,
		attemptsAfter,
		"a refused verification must not spend a code attempt",
	)

	// And the flow is untouched: still pending, still holding its one code, with
	// no credential written.
	state, err := db.GetPasswordResetVerificationState(ctx, verificationID)
	require.NoError(t, err)

	require.Equal(
		t,
		string(passwordreset.VerificationRequestPending),
		state.VerificationStatus,
		"a refused verification must not change the request's state",
	)
	require.Equal(t, int32(1), state.PinIssuedCount)

	counts, err := db.CountPasswordResetStateForEmail(ctx, emailAddress)
	require.NoError(t, err)

	require.Equal(t, int64(1), counts.VerificationCodeCount)
	require.Equal(t, int64(1), counts.PendingResetCount)
	require.Equal(t, int64(0), counts.CredentialCount)
}

// The budget belongs to the address, so one client's flood must not stop another
// behind the same proxy from verifying.
func TestPasswordResetIPRateLimit_VerifyOneAddressDoesNotSpendAnother(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const (
		flooder   = "198.51.100.98"
		neighbour = "198.51.100.99"
	)

	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := resetApp(t, limiter)

	for range passwordreset.VerifyIPRateLimitMax {
		postPinVerifyFrom(t, app, uuid.New(), flooder)
	}

	require.Equal(
		t,
		http.StatusTooManyRequests,
		postPinVerifyFrom(t, app, uuid.New(), flooder).status,
	)

	// Same moment, same endpoint, different address. If the counter were shared
	// across addresses this would be a 429 too.
	accepted := postPinVerifyFrom(t, app, uuid.New(), neighbour)

	require.Equal(
		t,
		http.StatusNotFound,
		accepted.status,
		accepted.code,
	)
}

// Verification and issuance are adjacent steps of one flow and are two different
// acts, so exhausting one must not spend the other's allowance. A caller who
// mistyped a PIN has to be able to ask for a replacement afterwards.
func TestPasswordResetIPRateLimit_VerifyAndIssuanceAreSeparate(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := resetApp(t, limiter)

	const address = "198.51.100.87"

	for range passwordreset.VerifyIPRateLimitMax {
		postPinVerifyFrom(t, app, uuid.New(), address)
	}

	require.Equal(
		t,
		http.StatusTooManyRequests,
		postPinVerifyFrom(t, app, uuid.New(), address).status,
		"the verification budget should be spent",
	)

	// The issuance budget is a different counter and is untouched. A caller who
	// burned their verification allowance can still ask for a new code.
	require.Zero(
		t,
		limiter.CallsIn(testutil.ResetPinIPNamespace),
		"spending the verify budget must not touch the issuance budget",
	)

	issued := postIssuanceFrom(
		t,
		app,
		uuid.New(),
		"/pin",
		address,
	)

	require.NotEqual(
		t,
		http.StatusTooManyRequests,
		issued.status,
		"issuance must remain available after the verify budget is spent: %s",
		issued.code,
	)
}

// countVerificationCodeAttempts reads the attempt counter off the live code row.
//
// The query is written inline rather than added to the sqlc test target, which is
// what auth_register_concurrency_test.go does for its equivalent counts: it has
// exactly one caller, and the generated test package is not the place for it.
func countVerificationCodeAttempts(
	t *testing.T,
	verificationID uuid.UUID,
) (int32, error) {
	t.Helper()

	var attempts int32

	err := testPool.QueryRow(
		context.Background(),
		`SELECT attempts
		 FROM verification_codes
		 WHERE verification_request_id = $1
		   AND consumed_at IS NULL
		   AND invalidated_at IS NULL`,
		verificationID,
	).Scan(&attempts)

	return attempts, err
}

// One address exhausting a reset budget must not lock anyone else out, which is
// the whole reason the budget is per address rather than global.
func TestPasswordResetIPRateLimit_OneAddressDoesNotSpendAnother(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := resetApp(t, limiter)

	const flooder = "198.51.100.90"
	const neighbour = "198.51.100.91"

	for range passwordreset.RequestIPRateLimitMax + 1 {
		postResetFrom(t, app, "neighbour-check@example.com", flooder)
	}

	require.Equal(
		t,
		http.StatusTooManyRequests,
		postResetFrom(t, app, "neighbour-check@example.com", flooder).status,
	)

	// Same moment, same endpoint, different address.
	require.Equal(
		t,
		http.StatusCreated,
		postResetFrom(t, app, "neighbour-check@example.com", neighbour).status,
	)
}

// postResetFinalizeFrom calls the finalization route from the given client
// address, carrying the given continuation token.
//
// It builds the request rather than reusing resetCall, because resetCall serves
// the routes that take no credential in a header and therefore never learned to
// send one. A finalization without the header is refused before it reaches the
// flow, which would make every budget assertion below meaningless.
func postResetFinalizeFrom(
	t *testing.T,
	app *fiber.App,
	token string,
	password string,
	address string,
) resetOutcome {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/finalize",
		strings.NewReader(`{"password":"`+password+`"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Password-Reset-Continuation", token)
	req.Header.Set("X-Forwarded-For", address)

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 60 * time.Second})
	require.NoError(t, err)

	outcome := resetOutcome{
		status:     resp.StatusCode,
		retryAfter: resp.Header.Get(fiber.HeaderRetryAfter),
	}

	if outcome.status < 400 {
		return outcome
	}

	var decoded registerAPIError

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&decoded))

	outcome.code = decoded.Error.Code

	return outcome
}

// openResetWithContinuation drives the whole password reset flow through the real
// endpoints on the shared app and returns a continuation token that production
// code minted, together with the account it belongs to.
//
// The flow runs on testApp rather than on the app under test so that arranging a
// fixture never spends the budget the test is about: testApp carries its own
// limiter instance, and its finalize counter is not the one being exhausted.
//
// A token the application issued is what makes the assertions below meaningful. A
// refusal that consumed it would turn a momentarily expensive route into a user
// who can never finish the reset, and only a real capability can show that.
func openResetWithContinuation(
	t *testing.T,
	emailAddress string,
) (uuid.UUID, string) {
	t.Helper()

	userID, verificationID, issuedPIN := startPasswordResetWithPIN(
		t,
		emailAddress,
	)

	minted := verifyPasswordResetPIN(verificationID, issuedPIN)
	require.NoError(t, minted.err)
	require.Equal(t, http.StatusOK, minted.status, minted.code)
	require.NotEmpty(t, minted.token)

	return userID, minted.token
}

// The finalize budget is spent before the handler runs.
//
// The route is the only one in this flow that performs an Argon2id derivation, so
// its requests are charged even when they are refused: a request carrying no
// continuation token never reaches the hashing step, and charging only the ones
// that got that far would leave the expensive path free to flood.
//
// Every request below is refused by the handler rather than by the budget, which
// is what makes the boundary observable: the first Max requests answer 401 and the
// next answers 429, so the two layers cannot be confused for one another.
func TestPasswordResetIPRateLimit_FinalizeBudgetIsEnforcedAheadOfTheHandler(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := resetApp(t, limiter)

	const address = "198.51.100.87"

	// No token is offered, so every request is a pure refusal that reaches the
	// header check and nothing else. Nothing can be consumed by any of them, which
	// is what keeps this a measurement of the budget rather than of the flow.
	for range passwordreset.FinalizeIPRateLimitMax {
		outcome := postResetFinalizeFrom(t, app, "", "new-password", address)

		require.Equal(
			t,
			http.StatusUnauthorized,
			outcome.status,
			"attempt within budget should reach the handler, got %s",
			outcome.code,
		)
	}

	exhausted := postResetFinalizeFrom(t, app, "", "new-password", address)

	require.Equal(t, http.StatusTooManyRequests, exhausted.status)
	require.Equal(t, middleware.CodeIPRateLimitExceeded, exhausted.code)

	// It was the finalize budget, charged against this address, under this
	// policy. Asserted from the limiter's own accounting rather than from the
	// status code, because a request refused by the wrong budget would also be a
	// 429.
	require.Equal(
		t,
		passwordreset.FinalizeIPRateLimitMax+1,
		limiter.CallsIn(testutil.ResetFinalizeIPNamespace),
	)
	// Every consultation was charged against this one address, which is what makes
	// the budget per-address rather than global.
	chargedTo := limiter.SubjectsIn(testutil.ResetFinalizeIPNamespace)
	require.Len(
		t,
		chargedTo,
		passwordreset.FinalizeIPRateLimitMax+1,
		"one subject per request, and no other",
	)

	for _, subject := range chargedTo {
		require.Equal(
			t,
			address,
			subject,
			"namespace charged the wrong address",
		)
	}

	require.Equal(
		t,
		ratelimit.Policy{
			Max:    passwordreset.FinalizeIPRateLimitMax,
			Window: passwordreset.FinalizeIPRateLimitWindow,
		},
		limiter.Policies[len(limiter.Policies)-1],
		"the policy must be the one this feature declares",
	)

	// Registration's identically-shaped budget is a different counter. The two
	// routes perform the same work, so merging them is the tempting mistake, and
	// it would let one flow's flood lock the other out.
	require.Zero(
		t,
		limiter.CallsIn(testutil.FinalizeIPNamespace),
		"password reset must not spend registration's finalize budget",
	)
}

// A refused finalization carries a wait the client can act on, and it is derived
// from the counter rather than from the configured window.
//
// The real limiter is used, because the wait is produced by the counter's own
// remaining TTL. A fake can be told what to report, which would only prove the
// fake reports what it was told — which is why this is a separate test from the
// one that walks the budget.
func TestPasswordResetIPRateLimit_FinalizeExceededCarriesRetryAfter(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	app, _ := newIPLimitedApp(t)

	const address = "198.51.100.92"

	var waits []int

	for range passwordreset.FinalizeIPRateLimitMax + 3 {
		outcome := postResetFinalizeFrom(
			t,
			app,
			"",
			"new-password",
			address,
		)

		if outcome.status == http.StatusUnauthorized {
			continue
		}

		require.Equal(t, http.StatusTooManyRequests, outcome.status)
		require.NotEmpty(
			t,
			outcome.retryAfter,
			"a 429 must tell the client when to come back",
		)

		seconds, err := strconv.Atoi(outcome.retryAfter)
		require.NoError(t, err)
		require.Positive(t, seconds)
		require.LessOrEqual(
			t,
			seconds,
			int(passwordreset.FinalizeIPRateLimitWindow.Seconds()),
			"the wait must never exceed the window it came from",
		)

		waits = append(waits, seconds)
	}

	require.NotEmpty(t, waits, "the budget was never spent")

	// A refused request does not push the window forward, so the reported wait can
	// only hold steady or fall.
	for index := 1; index < len(waits); index++ {
		require.LessOrEqualf(
			t,
			waits[index],
			waits[index-1],
			"a refused request must not extend the window: waits %v",
			waits,
		)
	}
}

// A refused finalization changes nothing.
//
// The budget is charged in front of the handler, so the request that is turned
// away never runs the transaction: the continuation stays unconsumed, the reset
// stays pending, and the credential is untouched. Reading the tables is what
// proves it, because a refusal is also what a consumed continuation would answer
// on the next attempt.
func TestPasswordResetIPRateLimit_FinalizeRefusalConsumesNothing(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "finalize-refusal@example.com"

	_, token := openResetWithContinuation(t, emailAddress)

	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := resetApp(t, limiter)

	const address = "198.51.100.88"

	for range passwordreset.FinalizeIPRateLimitMax {
		postResetFinalizeFrom(t, app, "", "new-password", address)
	}

	// A real, valid continuation presented from an address that has spent its
	// budget. This is the request that would complete the reset if anything let it
	// through.
	refused := postResetFinalizeFrom(
		t,
		app,
		token,
		"a-brand-new-password",
		address,
	)

	require.Equal(t, http.StatusTooManyRequests, refused.status)
	require.Equal(t, middleware.CodeIPRateLimitExceeded, refused.code)

	state, err := db.GetPasswordResetState(ctx, emailAddress)
	require.NoError(t, err)

	require.Equal(
		t,
		string(passwordreset.PendingPasswordResetPending),
		state.PasswordResetStatus,
		"a refused finalization must not complete the reset",
	)

	counts, err := db.CountPasswordResetStateForEmail(ctx, emailAddress)
	require.NoError(t, err)

	require.Zero(
		t,
		counts.CredentialCount,
		"a refused finalization must not write a credential",
	)

	// The capability is intact, so the very same request succeeds once the budget
	// reopens. That is what makes the Retry-After a real instruction rather than a
	// dead end, and it is only observable if the refusal consumed nothing: a
	// consumed continuation would answer 409 here instead.
	//
	// A fresh limiter stands in for a reopened window. Waiting one out would make
	// the test depend on a clock rather than on behaviour.
	reopened, _ := resetApp(t, testutil.NewCountingPinRateLimiter())

	accepted := postResetFinalizeFrom(
		t,
		reopened,
		token,
		"a-brand-new-password",
		address,
	)

	require.Equal(
		t,
		http.StatusOK,
		accepted.status,
		"a refused finalization must leave the continuation usable: %s",
		accepted.code,
	)

	finalState, err := db.GetPasswordResetFinalState(ctx, emailAddress)
	require.NoError(t, err)

	require.True(
		t,
		finalState.ConsumedAt.Valid,
		"the reset completed only once the budget reopened",
	)
	require.Equal(
		t,
		string(passwordreset.PendingPasswordResetCompleted),
		finalState.PasswordResetStatus,
	)
}

// One address spending the finalize budget must not lock another out, which is
// the whole reason the budget is per address rather than global.
func TestPasswordResetIPRateLimit_FinalizeOneAddressDoesNotSpendAnother(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "finalize-neighbour@example.com"

	_, token := openResetWithContinuation(t, emailAddress)

	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := resetApp(t, limiter)

	const (
		flooder  = "198.51.100.89"
		neighbour = "198.51.100.94"
	)

	for range passwordreset.FinalizeIPRateLimitMax {
		postResetFinalizeFrom(t, app, "", "new-password", flooder)
	}

	require.Equal(
		t,
		http.StatusTooManyRequests,
		postResetFinalizeFrom(t, app, "", "new-password", flooder).status,
	)

	// Same moment, same endpoint, same continuation, different address. If the
	// counter were shared across addresses this would be a 429 too, and the reset
	// would be unreachable behind one noisy neighbour.
	accepted := postResetFinalizeFrom(
		t,
		app,
		token,
		"a-brand-new-password",
		neighbour,
	)

	require.Equal(
		t,
		http.StatusOK,
		accepted.status,
		accepted.code,
	)

	finalState, err := db.GetPasswordResetFinalState(ctx, emailAddress)
	require.NoError(t, err)

	require.True(
		t,
		finalState.ConsumedAt.Valid,
		"the neighbour's finalization must have completed the reset",
	)
	require.Equal(
		t,
		string(passwordreset.PendingPasswordResetCompleted),
		finalState.PasswordResetStatus,
	)
}

// An unusable limiter refuses finalization rather than letting it through.
//
// Failing open would hand anyone the ability to switch the ceiling off by causing
// the failure that produces it. This widens the blast radius of a Redis outage,
// and the widening is deliberate: a request that reached the handler would
// perform an Argon2id derivation that nothing was counting.
func TestPasswordResetIPRateLimit_FinalizeRedisFailureFailsClosed(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "finalize-redis-down@example.com"

	_, token := openResetWithContinuation(t, emailAddress)

	limiter := testutil.UnavailablePinRateLimiter(
		errors.New("redis unreachable"),
	)

	app, _ := resetApp(t, limiter)

	outcome := postResetFinalizeFrom(
		t,
		app,
		token,
		"a-brand-new-password",
		"198.51.100.93",
	)

	require.Equal(t, http.StatusServiceUnavailable, outcome.status)
	require.Equal(t, middleware.CodeIPRateLimitUnavailable, outcome.code)

	// No fabricated wait: the counter has no window to name, so inventing one
	// would tell the client to come back at a moment nobody chose.
	require.Empty(t, outcome.retryAfter)

	state, err := db.GetPasswordResetState(ctx, emailAddress)
	require.NoError(t, err)

	require.Equal(
		t,
		string(passwordreset.PendingPasswordResetPending),
		state.PasswordResetStatus,
		"a refused finalization must not complete the reset",
	)

	counts, err := db.CountPasswordResetStateForEmail(ctx, emailAddress)
	require.NoError(t, err)

	require.Zero(
		t,
		counts.CredentialCount,
		"a limiter outage must not be a way to reset a password",
	)
}
