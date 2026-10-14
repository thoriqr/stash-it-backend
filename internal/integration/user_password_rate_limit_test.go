package integration_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/user"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
)

// The per-user budget on PUT /users/me/password, and the absence of one on the two
// profile routes.
//
// Two apps are used, because they prove different things and neither can stand in
// for the other. The shared testApp carries the production limiter over real
// Redis, so it proves the counter: that exactly Max requests are served, that the
// Retry-After comes from this counter's own window, and that a refusal is a real
// 429 rather than a fake's opinion. The counting app substitutes the limiter and
// proves the wiring: which namespace, which subject, which policy, and what an
// unusable limiter does.
//
// Every password request below is deliberately malformed. A request the handler
// refuses costs nothing, so walking a budget to exhaustion does not also pay for an
// Argon2id derivation, and the 400 a malformed body produces is the unambiguous
// marker that the limiter let the request through.

// userPasswordRateLimitSecret keys this file's real limiter, matching how
// search_user_rate_limit_test.go builds one.
const userPasswordRateLimitSecret = "user-password-rate-limit-test-secret"

// malformedPasswordBody is not JSON, so the handler answers 400 the moment it is
// reached and does nothing at all when it is not.
const malformedPasswordBody = "this is not json"

var errPasswordRateLimitRedis = errors.New("redis is unreachable")

type passwordLimitOutcome struct {
	status     int
	body       string
	code       string
	retryAfter string
}

// newUserRealLimiterApp builds an app over the shared test Redis with its limiter
// keys cleared, so one test cannot see another's counts.
//
// This is the app that proves the counter itself.
func newUserRealLimiterApp(t *testing.T) *fiber.App {
	t.Helper()

	client, err := testutil.StartRedisForTests(t.Context())
	require.NoError(t, err)

	keys, err := client.Keys(t.Context(), "rl:*").Result()
	require.NoError(t, err)

	if len(keys) > 0 {
		require.NoError(t, client.Del(t.Context(), keys...).Err())
	}

	app, _ := testutil.NewAppWithLimiter(
		testPool,
		ratelimit.New(client, []byte(userPasswordRateLimitSecret)),
	)

	return app
}

// profileOn and patchProfileOn drive the two profile routes against a
// caller-supplied app.
//
// They exist because the shared getProfile and patchProfile helpers in
// user_api_helpers_test.go always drive the package-level testApp, and these tests
// need the app carrying the counting limiter — the assertion that a profile
// request consults no budget can only be made from that limiter's accounting.
func profileOn(t *testing.T, app *fiber.App, token string) (int, []byte) {
	t.Helper()

	return doRequestOn(t, app, http.MethodGet, "/users/me", nil, bearer(token))
}

func patchProfileOn(
	t *testing.T,
	app *fiber.App,
	token string,
	rawBody []byte,
) (int, []byte) {
	t.Helper()

	return doRequestOn(t, app, http.MethodPatch, "/users/me", rawBody, bearer(token))
}

func doRequestOn(
	t *testing.T,
	app *fiber.App,
	method string,
	target string,
	body []byte,
	authorization string,
) (int, []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req := httptest.NewRequest(method, target, reader)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}

	resp, err := app.Test(req, fiber.TestConfig{Timeout: userRequestTimeout})
	require.NoError(t, err)

	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, raw
}

func passwordFrom(
	t *testing.T,
	app *fiber.App,
	token string,
	body string,
) passwordLimitOutcome {
	t.Helper()

	req := newPasswordRequest(t, token, body)

	resp, err := app.Test(req, fiber.TestConfig{Timeout: userRequestTimeout})
	require.NoError(t, err)

	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return passwordLimitOutcome{
		status:     resp.StatusCode,
		body:       string(raw),
		code:       decodeUserError(t, raw).Error.Code,
		retryAfter: resp.Header.Get(fiber.HeaderRetryAfter),
	}
}

// newJSONRequest builds a request with a JSON content type, which the shared
// request helpers in the user test files do not expose.
func newJSONRequest(
	t *testing.T,
	method string,
	target string,
	body []byte,
) *http.Request {
	t.Helper()

	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	return req
}

func newPasswordRequest(t *testing.T, token string, body string) *http.Request {
	t.Helper()

	req := newJSONRequest(t, http.MethodPut, "/users/me/password", []byte(body))

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return req
}

// walkPasswordBudget spends the whole allowance with malformed bodies and
// returns the outcome of the request that exceeded it.
//
// Exactly Max requests must reach the handler. The 400 each produces is the proof
// that the limiter admitted it, so a test that walks the budget cannot pass
// against a limiter that refused everything.
func walkPasswordBudget(t *testing.T, app *fiber.App, token string) passwordLimitOutcome {
	t.Helper()

	for served := range user.PasswordUserRateLimitMax {
		outcome := passwordFrom(t, app, token, malformedPasswordBody)

		require.Equal(
			t,
			http.StatusBadRequest,
			outcome.status,
			"request %d of %d should have reached the handler",
			served+1,
			user.PasswordUserRateLimitMax,
		)
	}

	return passwordFrom(t, app, token, malformedPasswordBody)
}

// --- against the real counter ----------------------------------------------

// Exactly Max requests are served and the next is refused.
//
// The number is the point: a limiter looser than the policy would let more
// through, and one tighter would refuse a request the declared budget allows.
func TestUserPasswordRateLimit_BudgetIsEnforcedAheadOfTheHandler(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	app := newUserRealLimiterApp(t)

	userID, session := seedUserWithoutCredential(
		t, ctx, "password-budget@example.com", "Password Budget",
	)

	outcome := walkPasswordBudget(t, app, newSessionAccessToken(t, userID, session))

	require.Equal(t, http.StatusTooManyRequests, outcome.status)
	require.Equal(t, "USER_RATE_LIMIT_EXCEEDED", outcome.code)
}

// The wait comes from this counter's own window: positive, no longer than the
// window it came from, and not pushed out by the requests that were refused.
//
// It is read against real Redis because a fake can be told whatever it likes.
func TestUserPasswordRateLimit_ExceededCarriesRetryAfterFromRealRedis(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	app := newUserRealLimiterApp(t)

	userID, session := seedUserWithoutCredential(
		t, ctx, "retry-after@example.com", "Retry After",
	)

	first := walkPasswordBudget(t, app, newSessionAccessToken(t, userID, session))
	require.Equal(t, http.StatusTooManyRequests, first.status)
	require.NotEmpty(t, first.retryAfter, "a refusal must say when to come back")

	seconds, err := strconv.Atoi(first.retryAfter)
	require.NoError(t, err)
	require.Positive(t, seconds)
	require.LessOrEqual(
		t,
		time.Duration(seconds)*time.Second,
		user.PasswordUserRateLimitWindow,
		"the wait may not exceed the window it came from",
	)

	// Further refusals must not extend the window.
	second := passwordFrom(t, app, newSessionAccessToken(t, userID, session), malformedPasswordBody)
	require.Equal(t, http.StatusTooManyRequests, second.status)

	secondSeconds, err := strconv.Atoi(second.retryAfter)
	require.NoError(t, err)
	require.LessOrEqual(
		t,
		secondSeconds,
		seconds,
		"a refused request must not extend the window",
	)
}

// A refused request never reaches the derivations it guards, so it stores nothing
// and revokes nothing. That is the property a budget mounted in front of the
// handler exists to provide, and it is asserted from the tables.
func TestUserPasswordRateLimit_ARefusedRequestChangesNothing(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	app := newUserRealLimiterApp(t)

	userID, session, storedBefore := seedUserWithCredential(
		t, ctx, "refused-writes@example.com", "Refused Writes", "old-password-value",
	)

	other, err := db.CreateSessionForUser(ctx, userID)
	require.NoError(t, err)

	outcome := walkPasswordBudget(t, app, newSessionAccessToken(t, userID, session))
	require.Equal(t, http.StatusTooManyRequests, outcome.status)

	storedAfter, err := db.GetPasswordCredentialHashForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, storedBefore, storedAfter, "no refused request may write a credential")

	active, err := db.CountActiveSessionsForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(2), active, "no refused request may revoke a session")

	revoked, err := db.IsSessionRevoked(ctx, other)
	require.NoError(t, err)
	require.False(t, revoked)

	// And the credential still works, which distinguishes "nothing was changed"
	// from "something was changed and then consumed".
	require.True(t, passwordHashStillVerifies(t, ctx, storedAfter, "old-password-value"))
}

// A failed attempt costs the budget exactly as a successful one does.
//
// The change branch verifies a submitted password, and bounding those attempts is
// what the policy is for. Nothing on this route releases a charge.
func TestUserPasswordRateLimit_FailedAttemptsStillSpendTheBudget(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	app := newUserRealLimiterApp(t)

	userID, session, storedBefore := seedUserWithCredential(
		t, ctx, "failures-spend@example.com", "Failures Spend", "old-password-value",
	)

	token := newSessionAccessToken(t, userID, session)

	for attempt := range user.PasswordUserRateLimitMax {
		outcome := passwordFrom(t, app, token,
			`{"current_password":"not-the-password","new_password":"attacker-chosen"}`)

		require.Equal(
			t,
			http.StatusUnauthorized,
			outcome.status,
			"attempt %d should reach the service and be refused there",
			attempt+1,
		)
		require.Equal(t, "CURRENT_PASSWORD_INVALID", outcome.code)
	}

	outcome := passwordFrom(t, app, token,
		`{"current_password":"not-the-password","new_password":"attacker-chosen"}`)

	require.Equal(
		t,
		http.StatusTooManyRequests,
		outcome.status,
		"failed attempts must still consume the budget",
	)

	// The credential was never replaced, so the budget bounded guessing rather
	// than turning guesses into writes.
	storedAfter, err := userTestDB().GetPasswordCredentialHashForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, storedBefore, storedAfter)
}

// The budget belongs to one account. An address-keyed budget could not say this.
func TestUserPasswordRateLimit_OneAccountDoesNotSpendAnother(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	app := newUserRealLimiterApp(t)

	noisyID, noisySession := seedUserWithoutCredential(
		t, ctx, "noisy@example.com", "Noisy",
	)
	neighbourID, neighbourSession := seedUserWithoutCredential(
		t, ctx, "neighbour@example.com", "Neighbour",
	)

	refused := walkPasswordBudget(t, app, newSessionAccessToken(t, noisyID, noisySession))
	require.Equal(t, http.StatusTooManyRequests, refused.status)

	// The neighbour still reaches the handler, and can still change its password.
	outcome := passwordFrom(t, app, newSessionAccessToken(t, neighbourID, neighbourSession),
		malformedPasswordBody)
	require.Equal(
		t,
		http.StatusBadRequest,
		outcome.status,
		"one account's budget must not refuse another",
	)

	changed := passwordFrom(t, app, newSessionAccessToken(t, neighbourID, neighbourSession),
		`{"new_password":"neighbour-new-password"}`)
	require.Equal(t, http.StatusOK, changed.status)
}

// A spent password budget must not lock anyone out of signing in or out of
// recovering their account. That is the reason this namespace is its own, and it
// is asserted against the two flows most at risk of sharing a counter with it.
func TestUserPasswordRateLimit_SpendsNoOtherFlowsBudget(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	app := newUserRealLimiterApp(t)

	const emailAddress = "namespaces@example.com"

	userID, session, _ := seedUserWithCredential(
		t, ctx, emailAddress, "Namespaces", "old-password-value",
	)

	refused := walkPasswordBudget(t, app, newSessionAccessToken(t, userID, session))
	require.Equal(t, http.StatusTooManyRequests, refused.status)

	// Login is the flow most likely to be affected by mistake: a password route
	// and a login route are both about passwords.
	status, _ := loginManually(t, emailAddress, "old-password-value")
	require.Equal(
		t,
		http.StatusOK,
		status,
		"a spent password budget must not lock anyone out of signing in",
	)

	// And password reset still opens, which is the recovery path a user has when
	// this budget refuses them.
	status, _ = requestPasswordReset(t, emailAddress)
	require.Equal(t, http.StatusCreated, status)
}

// --- against a limiter the test controls ------------------------------------

// The declared namespace, subject and policy are asserted from the limiter's own
// accounting rather than from response codes: a request refused by the wrong
// budget would still be a 429, and only the namespace shows which counter was
// spent.
func TestUserPasswordRateLimit_ChargesOnlyItsOwnNamespace(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	limiter := testutil.NewCountingPinRateLimiter()
	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	userID, session := seedUserWithoutCredential(
		t, ctx, "namespace@example.com", "Namespace",
	)

	token := newSessionAccessToken(t, userID, session)

	outcome := passwordFrom(t, app, token, malformedPasswordBody)
	require.Equal(t, http.StatusBadRequest, outcome.status)

	require.Equal(t, 1, limiter.CallsIn(testutil.UserPasswordUserNamespace))
	require.Equal(
		t,
		[]string{testutil.UserPasswordUserNamespace},
		limiter.Namespaces,
	)
	require.Equal(
		t,
		[]string{userID.String()},
		limiter.Subjects,
		"the subject must be the verified account, never a client address",
	)

	require.Len(t, limiter.Policies, 1)
	require.Equal(t, int64(user.PasswordUserRateLimitMax), limiter.Policies[0].Max)
	require.Equal(t, user.PasswordUserRateLimitWindow, limiter.Policies[0].Window)

	// A profile request consults nothing at all.
	status, _ := profileOn(t, app, token)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, 1, limiter.CallsIn(testutil.UserPasswordUserNamespace))
}

// Fail-closed is preserved. An unreachable counter is a refusal, not permission.
//
// The app is handed a limiter that cannot answer rather than a broken connection;
// the limiter package's own tests already prove the production limiter reports an
// error when Redis is gone, so what is under test here is the route's behaviour
// given that error.
func TestUserPasswordRateLimit_UnavailableLimiterRefusesBeforeTheHandler(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	app, _ := testutil.NewAppWithLimiter(
		testPool,
		testutil.UnavailablePinRateLimiter(errPasswordRateLimitRedis),
	)

	userID, session, storedBefore := seedUserWithCredential(
		t, ctx, "limiter-down@example.com", "Limiter Down", "old-password-value",
	)

	other, err := db.CreateSessionForUser(ctx, userID)
	require.NoError(t, err)

	token := newSessionAccessToken(t, userID, session)

	outcome := passwordFrom(t, app, token,
		`{"current_password":"old-password-value","new_password":"a-new-password"}`)

	require.Equal(t, http.StatusServiceUnavailable, outcome.status)
	require.Equal(t, middleware.CodeUserRateLimitUnavailable, outcome.code)

	// The handler's own answer is absent, so it never ran.
	require.NotContains(t, outcome.body, user.CodePasswordWorkUnavailable)

	// No window to name, and inventing one would tell the caller to come back at a
	// moment nobody chose.
	require.Empty(t, outcome.retryAfter, "an unusable budget must not report a wait")

	storedAfter, err := db.GetPasswordCredentialHashForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, storedBefore, storedAfter, "an unavailable budget must write nothing")

	active, err := db.CountActiveSessionsForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(2), active, "an unavailable budget must revoke nothing")

	revoked, err := db.IsSessionRevoked(ctx, other)
	require.NoError(t, err)
	require.False(t, revoked)

	// The profile routes are untouched by the same failure. That is the whole
	// reason they carry no budget of their own.
	status, _ := profileOn(t, app, token)
	require.Equal(t, http.StatusOK, status)
}

// --- the profile routes are unlimited --------------------------------------

// Both profile routes spend no budget at all. Not merely that they are cheap, but
// that no counter is consulted — and they send far more requests than the policy
// would allow, so a limit of any size would have shown up.
func TestUserProfileRoutes_ConsultNoBudget(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	limiter := testutil.NewCountingPinRateLimiter()
	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	userID, _, _ := seedUserWithCredential(
		t, ctx, "profile-unlimited@example.com", "Profile Unlimited", "some-password",
	)

	token := newTestAccessToken(t, userID)

	for range user.PasswordUserRateLimitMax * 4 {
		status, _ := profileOn(t, app, token)
		require.Equal(t, http.StatusOK, status)

		status, _ = patchProfileOn(t, app, token, []byte(`{"display_name":"Renamed"}`))
		require.Equal(t, http.StatusOK, status)
	}

	require.Empty(t, limiter.Namespaces, "no profile request may consult a budget")
}

// An unreachable limiter does not take the profile routes offline, because they
// never ask it anything. This is the availability property leaving them unlimited
// was chosen for.
func TestUserProfileRoutes_SurviveAnUnusableLimiter(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	limiter := testutil.UnavailablePinRateLimiter(errPasswordRateLimitRedis)
	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	userID, _, _ := seedUserWithCredential(
		t, ctx, "profile-resilient@example.com", "Profile Resilient", "some-password",
	)

	token := newTestAccessToken(t, userID)

	for range user.PasswordUserRateLimitMax {
		status, _ := profileOn(t, app, token)
		require.Equal(t, http.StatusOK, status)

		status, _ = patchProfileOn(t, app, token, []byte(`{"display_name":"Renamed"}`))
		require.Equal(t, http.StatusOK, status)
	}

	require.Empty(t, limiter.Namespaces)
}

// A spent password budget does not affect reading or renaming a profile. The
// namespaces are separate, so one flow's flood cannot spend another's allowance.
func TestUserProfileRoutes_UnaffectedByASpentPasswordBudget(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	userID, session := seedUserWithoutCredential(
		t, ctx, "separate-namespaces@example.com", "Separate Namespaces",
	)

	passwordToken := newSessionAccessToken(t, userID, session)

	refused := walkPasswordBudget(t, app, passwordToken)
	require.Equal(t, http.StatusTooManyRequests, refused.status)

	profileToken := newTestAccessToken(t, userID)

	status, _ := profileOn(t, app, profileToken)
	require.Equal(t, http.StatusOK, status)

	status, _ = patchProfileOn(t, app, profileToken, []byte(`{"display_name":"Still Fine"}`))
	require.Equal(t, http.StatusOK, status)
}

// An unauthenticated request never reaches the budget and is never charged to any
// account. The subject is a verified identity, and there is nothing to key on
// before middleware.Auth has run.
func TestUserPasswordRateLimit_UnauthenticatedRequestsSpendNoBudget(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	limiter := testutil.NewCountingPinRateLimiter()
	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	for range user.PasswordUserRateLimitMax + 3 {
		outcome := passwordFrom(t, app, "", malformedPasswordBody)
		require.Equal(t, http.StatusUnauthorized, outcome.status)
	}

	require.Empty(
		t,
		limiter.Namespaces,
		"an unauthenticated request must not be charged to any account",
	)
}

// passwordHashStillVerifies reports whether a stored hash still accepts a
// password. It is the external check that a refused request did not disturb the
// credential, rather than a comparison against a value the test captured.
func passwordHashStillVerifies(
	t *testing.T,
	ctx context.Context,
	storedHash string,
	password string,
) bool {
	t.Helper()

	verification, err := testutil.NewPasswordHasher().Verify(ctx, password, storedHash)
	require.NoError(t, err)

	return verification.Match
}

// requestPasswordReset opens a reset for an address, so a test can show the
// recovery path survives a spent password budget.
func requestPasswordReset(t *testing.T, email string) (int, []byte) {
	t.Helper()

	return doUserRequest(
		t,
		http.MethodPost,
		"/auth/password-reset",
		[]byte(`{"email":"`+email+`"}`),
		"",
	)
}
