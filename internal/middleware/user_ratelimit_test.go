package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

const (
	userTestNamespace = "saved-item-enrich-user"
	userTestSubject   = "01a0f359-093b-737a-963a-80f7ca6768ed"
	userTestSession   = "01a0f359-093b-737a-963a-80f7ca6768ee"

	// userTestSecret is the access token secret the test verifier and the test
	// tokens share.
	userTestSecret = "user-rate-limit-test-secret"
)

// userTestPolicy is the budget these tests mount, declared as a value because a
// struct is not a constant.
var userTestPolicy = ratelimit.Policy{Max: 10, Window: time.Minute}

// userRecordingLimiter answers with a fixed result and remembers every subject it
// was asked about.
//
// The subjects are the subject of these tests: the point is which identity the
// middleware decided the request belonged to, and that is only visible on the far
// side of the call.
type userRecordingLimiter struct {
	allowed    bool
	err        error
	retryAfter time.Duration

	subjects   []string
	namespaces []string
	policies   []ratelimit.Policy
}

func (l *userRecordingLimiter) Allow(
	_ context.Context,
	namespace string,
	subject string,
	policy ratelimit.Policy,
) (ratelimit.Result, error) {
	l.subjects = append(l.subjects, subject)
	l.namespaces = append(l.namespaces, namespace)
	l.policies = append(l.policies, policy)

	if l.err != nil {
		return ratelimit.Result{}, l.err
	}

	return ratelimit.Result{Allowed: l.allowed, RetryAfter: l.retryAfter}, nil
}

// userRouteApp mounts the limiter exactly where production mounts it: after Auth,
// before the handler.
//
// The order is the property under test, so the route carries the real Auth
// middleware and a real access token rather than a stub that puts claims into the
// request for it. A limiter mounted before Auth would have no claims to read.
func userRouteApp(t *testing.T, limiter middleware.RateLimiter) *fiber.App {
	t.Helper()

	return userRouteAppWithHandler(
		t,
		limiter,
		func(c fiber.Ctx) error { return c.SendString("reached the handler") },
	)
}

func userRouteAppWithHandler(
	t *testing.T,
	limiter middleware.RateLimiter,
	handler fiber.Handler,
) *fiber.App {
	t.Helper()

	verifier := security.NewAccessTokenVerifier([]byte(userTestSecret))

	app := fiber.New(fiber.Config{ErrorHandler: testErrorHandler})

	app.Post(
		"/limited",
		middleware.Auth(verifier),
		middleware.UserRateLimit(limiter, userTestNamespace, userTestPolicy),
		handler,
	)

	return app
}

// userTestToken mints a real access token for the given subject.
//
// Minting it rather than stubbing claims is what makes these tests meaningful: the
// subject the middleware reads is one that actually came out of a verified token,
// through the same verifier production uses.
func userTestToken(t *testing.T, subject string) string {
	t.Helper()

	generator := security.NewAccessTokenGenerator([]byte(userTestSecret))

	token, err := generator.Generate(
		uuid.MustParse(subject),
		uuid.MustParse(userTestSession),
		15*time.Minute,
	)
	require.NoError(t, err)

	return token
}

func doUserRequest(
	t *testing.T,
	app *fiber.App,
	token string,
) *http.Response {
	t.Helper()

	return doUserRequestTo(t, app, "/limited", token)
}

func doUserRequestTo(
	t *testing.T,
	app *fiber.App,
	path string,
	token string,
) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, nil)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := app.Test(req)
	require.NoError(t, err)

	return resp
}

func userDecodeError(t *testing.T, resp *http.Response) apiError {
	t.Helper()

	var body apiError

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	return body
}

// A request within budget reaches the handler, keyed on the verified user.
//
// The subject asserted is the token's own subject. Nothing else about the request
// — not the path, not the address it came from — may influence which budget is
// charged.
func TestUserRateLimit_WithinBudgetReachesTheHandler(t *testing.T) {
	limiter := &userRecordingLimiter{allowed: true}

	app := userRouteApp(t, limiter)

	resp := doUserRequest(t, app, userTestToken(t, userTestSubject))

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, resp.Header.Get(fiber.HeaderRetryAfter))
	require.Equal(t, []string{userTestSubject}, limiter.subjects)
}

// Two different accounts are two budgets, even from one client address.
//
// This is the reason the limiter keys on the user: behind one shared address, or
// from one caller changing address, one account's flood must not stop another's.
func TestUserRateLimit_DistinctUsersAreDistinctBudgets(t *testing.T) {
	limiter := &userRecordingLimiter{allowed: true}

	app := userRouteApp(t, limiter)

	const other = "01a0f359-093b-737a-963a-80f7ca6768ef"

	for _, subject := range []string{userTestSubject, other, userTestSubject} {
		resp := doUserRequest(t, app, userTestToken(t, subject))
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}

	require.Equal(
		t,
		[]string{userTestSubject, other, userTestSubject},
		limiter.subjects,
	)
}

// An exhausted budget is a 429, and the wait reaches the response as a header
// rather than only as a field on an error object nobody serializes.
func TestUserRateLimit_ExceededIs429WithRetryAfter(t *testing.T) {
	limiter := &userRecordingLimiter{
		allowed:    false,
		retryAfter: 41 * time.Second,
	}

	app := userRouteApp(t, limiter)

	resp := doUserRequest(t, app, userTestToken(t, userTestSubject))

	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.Equal(t, "41", resp.Header.Get(fiber.HeaderRetryAfter))

	body := userDecodeError(t, resp)
	require.Equal(t, middleware.CodeUserRateLimitExceeded, body.Error.Code)

	// The body must not leak how the subject is keyed.
	require.NotContains(t, body.Error.Message, userTestSubject)
}

// The header carries whole seconds and never a fraction, because a client told to
// retry in half a second would retry before the window closed.
func TestUserRateLimit_RetryAfterIsWholeSeconds(t *testing.T) {
	cases := map[string]struct {
		retryAfter time.Duration
		want       string
	}{
		"exactly a second":        {retryAfter: time.Second, want: "1"},
		"a fraction of a second":  {retryAfter: 1500 * time.Millisecond, want: "2"},
		"no wait at all":          {retryAfter: 0, want: "0"},
		"a negative wait is zero": {retryAfter: -5 * time.Second, want: "0"},
		"a minute exactly":        {retryAfter: 60 * time.Second, want: "60"},
		"less than a minute":      {retryAfter: 59*time.Second + 999*time.Millisecond, want: "60"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			limiter := &userRecordingLimiter{
				allowed:    false,
				retryAfter: tc.retryAfter,
			}

			app := userRouteApp(t, limiter)

			resp := doUserRequest(t, app, userTestToken(t, userTestSubject))

			require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
			require.Equal(t, tc.want, resp.Header.Get(fiber.HeaderRetryAfter))
		})
	}
}

// A counter that cannot be evaluated is not permission. Letting it through would
// let anyone remove the ceiling by causing the failure that produces it.
//
// The 503 carries no Retry-After: there is no known recovery time, and naming one
// would tell the caller to come back at a moment nobody chose.
func TestUserRateLimit_UnavailableIs503AndDoesNotFabricateAWait(t *testing.T) {
	limiter := &userRecordingLimiter{err: errors.New("dial tcp: connection refused")}

	app := userRouteApp(t, limiter)

	resp := doUserRequest(t, app, userTestToken(t, userTestSubject))

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Empty(
		t,
		resp.Header.Get(fiber.HeaderRetryAfter),
		"a 503 has no known recovery time and must not name one",
	)

	body := userDecodeError(t, resp)
	require.Equal(t, middleware.CodeUserRateLimitUnavailable, body.Error.Code)
}

// A refused request must not run the handler. This is what makes the limiter a
// bound on work rather than only on responses.
func TestUserRateLimit_RefusalDoesNotReachTheHandler(t *testing.T) {
	limiter := &userRecordingLimiter{allowed: false}

	reached := 0

	app := userRouteAppWithHandler(t, limiter, func(c fiber.Ctx) error {
		reached++

		return c.SendString("reached the handler")
	})

	resp := doUserRequest(t, app, userTestToken(t, userTestSubject))

	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.Zero(t, reached, "a refused request must not perform the guarded work")
}

// A request with no token never reaches the limiter at all.
//
// Auth runs first and refuses it, so there is no identity to key on. This is what
// stops a caller reaching an unbounded route by omitting its credentials.
func TestUserRateLimit_UnauthenticatedRequestIsRefusedBeforeTheLimiter(t *testing.T) {
	limiter := &userRecordingLimiter{allowed: true}

	app := userRouteApp(t, limiter)

	for name, header := range map[string]string{
		"no header":          "",
		"wrong scheme":       "Basic abc123",
		"bearer with no uid": "Bearer ",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/limited", nil)

			if header != "" {
				req.Header.Set("Authorization", header)
			}

			resp, err := app.Test(req)
			require.NoError(t, err)

			require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		})
	}

	require.Empty(
		t,
		limiter.subjects,
		"the limiter must never be asked about a request with no identity",
	)
}

// Being unauthenticated is not a rate limit, and must not be answered as one: a
// caller told to wait for a budget nobody can spend has been given an instruction
// it cannot follow.
func TestUserRateLimit_UnauthenticatedRefusalIsNotARateLimit(t *testing.T) {
	limiter := &userRecordingLimiter{allowed: false}

	app := userRouteApp(t, limiter)

	resp := doUserRequest(t, app, "")

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	body := userDecodeError(t, resp)
	require.NotEqual(t, middleware.CodeUserRateLimitExceeded, body.Error.Code)
	require.NotEqual(t, middleware.CodeUserRateLimitUnavailable, body.Error.Code)
}

// The configured policy and namespace are passed through, so an endpoint's budget
// is decided in one place rather than inferred by the middleware.
func TestUserRateLimit_ConfiguredPolicyAndNamespaceAreApplied(t *testing.T) {
	limiter := &userRecordingLimiter{allowed: true}

	app := userRouteApp(t, limiter)

	doUserRequest(t, app, userTestToken(t, userTestSubject))

	require.Equal(t, []string{userTestNamespace}, limiter.namespaces)
	require.Equal(t, []ratelimit.Policy{userTestPolicy}, limiter.policies)
}

// A request with no verified claims is a fault, not a refusal and not permission.
//
// It is unreachable when the middleware is mounted where it is meant to be, so
// reaching it means the limiter is in front of a route that never authenticated
// anybody. Inventing a subject would merge every such request into one counter and
// hand a caller the choice of which budget it lands in.
func TestUserRateLimit_MissingClaimsIsAFaultAndIsNeverASubject(t *testing.T) {
	limiter := &userRecordingLimiter{allowed: true}

	app := fiber.New(fiber.Config{ErrorHandler: testErrorHandler})

	app.Post(
		"/unguarded",
		middleware.UserRateLimit(limiter, userTestNamespace, userTestPolicy),
		func(c fiber.Ctx) error { return c.SendString("reached the handler") },
	)

	resp := doUserRequestTo(t, app, "/unguarded", "")

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Empty(
		t,
		limiter.subjects,
		"a request with no identity must never be asked about as a subject",
	)
	require.Equal(t, apperror.CodeInternal, userDecodeError(t, resp).Error.Code)
}

// Mounting the limiter before Auth is a wiring mistake with a visible symptom:
// there is no claims value to read, so the route stops working rather than merely
// being unbounded. Asserted because it is the failure a reader is most likely to
// introduce when reordering the two.
func TestUserRateLimit_MountedBeforeAuthTheRouteCannotWork(t *testing.T) {
	limiter := &userRecordingLimiter{allowed: true}

	verifier := security.NewAccessTokenVerifier([]byte(userTestSecret))

	app := fiber.New(fiber.Config{ErrorHandler: testErrorHandler})

	app.Post(
		"/misordered",
		middleware.UserRateLimit(limiter, userTestNamespace, userTestPolicy),
		middleware.Auth(verifier),
		func(c fiber.Ctx) error { return c.SendString("reached the handler") },
	)

	resp := doUserRequestTo(t, app, "/misordered", userTestToken(t, userTestSubject))

	require.Equal(
		t,
		http.StatusInternalServerError,
		resp.StatusCode,
		"a limiter ahead of Auth has no identity to key on",
	)
	require.Empty(t, limiter.subjects)
}
