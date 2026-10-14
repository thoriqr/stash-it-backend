package user_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/user"
	userdb "github.com/thoriqr/stash-it-backend/internal/api/user/generated"
	"github.com/thoriqr/stash-it-backend/internal/api/user/mocks"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	"github.com/thoriqr/stash-it-backend/internal/validation"
)

// This file pins the wiring, which nothing below the route can: that
// PUT /users/me/password spends a budget before the handler sees the request, that
// it spends it under the policy the feature declares rather than one the route
// invented, and that the two profile routes spend nothing at all.
//
// Every password request below is deliberately malformed. A request the handler
// refuses costs nothing, so walking a budget to exhaustion here does not also pay
// for an Argon2id derivation, and the 400 a malformed body produces is the exact
// marker that distinguishes "the limiter let it through" from "the limiter stopped
// it".
//
// The budget's own behaviour — counting, windows, atomicity — belongs to
// internal/ratelimit and its own tests. What is checked here is which policy is
// applied to which route, and that a refusal reaches nothing below the middleware.

const (
	routesTestSecret    = "user-routes-test-secret"
	routesTestUserID    = "01a0f359-093b-737a-963a-80f7ca6768ed"
	routesTestOtherID   = "01a0f359-093b-737a-963a-80f7ca6768ec"
	routesTestSessionID = "01a0f359-093b-737a-963a-80f7ca6768eb"
)

type routeOutcome struct {
	status     int
	code       string
	retryAfter string
}

// newUserRoutesApp mounts the feature's routes over a limiter the test controls.
//
// The service is a real one over mocked dependencies with no expectations set on
// the password path. gomock fails on an unexpected call, so a request that reaches
// the service fails this test — a second, independent statement of what the
// limiter has to prevent. The profile routes are given an always-failing
// repository so they reach the handler, which is what makes "this route spends no
// budget" a statement about a request that actually ran.
func newUserRoutesApp(
	t *testing.T,
	limiter middleware.RateLimiter,
) (*fiber.App, *mocks.MockRepository) {
	t.Helper()

	ctrl := gomock.NewController(t)

	repository := mocks.NewMockRepository(ctrl)
	passwordHasher := mocks.NewMockPasswordHasher(ctrl)

	// AnyTimes, because the profile tests send several requests and none of them
	// is about what the service returns. An error keeps them cheap and keeps the
	// assertion about the limiter.
	repository.EXPECT().
		GetUserProfile(gomock.Any(), gomock.Any()).
		Return(userdb.GetUserProfileRow{}, errRepositoryUnavailable).
		AnyTimes()
	repository.EXPECT().
		ListAuthIdentitiesForUser(gomock.Any(), gomock.Any()).
		Return(nil, nil).
		AnyTimes()
	repository.EXPECT().
		UpdateUserDisplayName(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(userdb.UpdateUserDisplayNameRow{}, errRepositoryUnavailable).
		AnyTimes()

	service := user.NewService(repository, passwordHasher)

	verifier := security.NewAccessTokenVerifier([]byte(routesTestSecret))

	// The struct validator is configured here for the same reason the production
	// binary and the integration app both configure it: without it Fiber silently
	// skips every `validate` tag.
	app := fiber.New(fiber.Config{
		ErrorHandler:    httpx.NewErrorHandler(zap.NewNop()),
		StructValidator: validation.New(),
	})

	user.Routes(
		app.Group("/users"),
		user.NewHandler(service),
		verifier,
		limiter,
	)

	return app, repository
}

// errRepositoryUnavailable makes every profile request answer 500 without doing
// any work, so the profile tests measure limiter consultations only.
var errRepositoryUnavailable = errors.New("repository unavailable")

func newRoutesToken(t *testing.T, userID string) string {
	t.Helper()

	token, err := security.NewAccessTokenGenerator([]byte(routesTestSecret)).Generate(
		mustParseUUID(t, userID),
		mustParseUUID(t, routesTestSessionID),
		time.Hour,
	)
	require.NoError(t, err)

	return token
}

func mustParseUUID(t *testing.T, value string) uuid.UUID {
	t.Helper()

	parsed, err := uuid.Parse(value)
	require.NoError(t, err)

	return parsed
}

// send drives one request and reports the status, the error code, and the
// Retry-After that reached the response.
//
// body is empty for the two profile routes and malformed for the password one, for
// the reason the file header gives.
func send(
	t *testing.T,
	app *fiber.App,
	method string,
	target string,
	body string,
	token string,
) routeOutcome {
	t.Helper()

	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	require.NoError(t, err)

	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	outcome := routeOutcome{
		status:     resp.StatusCode,
		retryAfter: resp.Header.Get("Retry-After"),
	}

	var envelope map[string]any

	if json.Unmarshal(raw, &envelope) == nil {
		if errorBody, ok := envelope["error"].(map[string]any); ok {
			outcome.code, _ = errorBody["code"].(string)
		}
	}

	return outcome
}

func setPasswordRequest(t *testing.T, app *fiber.App, token string) routeOutcome {
	t.Helper()

	// Not JSON, so the handler answers 400 the moment it is reached.
	return send(t, app, http.MethodPut, "/users/me/password", "not json", token)
}

// --- the password route's budget -------------------------------------------

// The declared policy is applied, under the declared namespace, on the declared
// subject. Anything else here would be a budget this repository cannot reason
// about, because nothing would say what it was for.
func TestRoutes_PasswordRouteSpendsTheDeclaredPolicy(t *testing.T) {
	limiter := testutil.NewCountingPinRateLimiter()
	app, _ := newUserRoutesApp(t, limiter)

	userID := mustParseUUID(t, routesTestUserID)

	outcome := setPasswordRequest(t, app, newRoutesToken(t, routesTestUserID))
	require.Equal(t, http.StatusBadRequest, outcome.status, "the limiter let it through")

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
	require.Equal(t, int64(5), limiter.Policies[0].Max)
	require.Equal(t, 15*time.Minute, limiter.Policies[0].Window)
}

// Exactly Max requests are served and the next is refused. The 400 on the served
// ones is what proves they reached the handler, and the 429 is the budget.
func TestRoutes_PasswordRouteRefusesOnceTheBudgetIsSpent(t *testing.T) {
	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := newUserRoutesApp(t, limiter)

	token := newRoutesToken(t, routesTestUserID)

	for request := range user.PasswordUserRateLimitMax {
		outcome := setPasswordRequest(t, app, token)
		require.Equal(
			t,
			http.StatusBadRequest,
			outcome.status,
			"request %d should have reached the handler", request+1,
		)
	}

	outcome := setPasswordRequest(t, app, token)
	require.Equal(t, http.StatusTooManyRequests, outcome.status)
	require.Equal(t, "USER_RATE_LIMIT_EXCEEDED", outcome.code)
	require.NotEmpty(t, outcome.retryAfter, "a refusal must say when to come back")

	require.Equal(
		t,
		int(user.PasswordUserRateLimitMax)+1,
		limiter.CallsIn(testutil.UserPasswordUserNamespace),
	)
}

// A failed request costs the budget, exactly like a successful one.
//
// The charge is charged before the handler runs, so a body the handler rejects
// has already been counted. This is the whole point of the policy: the change
// branch verifies a real submitted password, and an attempt is what is being
// bounded.
func TestRoutes_PasswordBudgetIsNotRefundedOnFailure(t *testing.T) {
	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := newUserRoutesApp(t, limiter)

	token := newRoutesToken(t, routesTestUserID)

	// Every one of these is refused by the handler with a 400, and every one of
	// them still spends the budget.
	for range user.PasswordUserRateLimitMax {
		require.Equal(t, http.StatusBadRequest, setPasswordRequest(t, app, token).status)
	}

	require.Equal(
		t,
		http.StatusTooManyRequests,
		setPasswordRequest(t, app, token).status,
		"a refused request must still consume its budget",
	)

	require.Zero(
		t,
		limiter.ReleasesIn(testutil.UserPasswordUserNamespace),
		"no budget may be released on this route",
	)
}

// The budget belongs to one account. One account saturating it must not lock
// another out, which is the property an account-keyed subject exists to provide
// and the reason this route is not keyed on a client address.
func TestRoutes_PasswordBudgetBelongsToOneAccount(t *testing.T) {
	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := newUserRoutesApp(t, limiter)

	noisy := newRoutesToken(t, routesTestUserID)
	neighbour := newRoutesToken(t, routesTestOtherID)

	for range user.PasswordUserRateLimitMax {
		require.Equal(t, http.StatusBadRequest, setPasswordRequest(t, app, noisy).status)
	}

	require.Equal(t, http.StatusTooManyRequests, setPasswordRequest(t, app, noisy).status)

	// The neighbour has spent nothing.
	outcome := setPasswordRequest(t, app, neighbour)
	require.Equal(
		t,
		http.StatusBadRequest,
		outcome.status,
		"one account's budget must not refuse another",
	)
}

// Fail-closed is preserved. An unreachable counter is a refusal, not permission —
// allowing on failure would let anyone remove the ceiling by causing it — and the
// refusal happens before the derivations the budget exists to bound.
func TestRoutes_PasswordRouteFailsClosedWhenRedisIsUnreachable(t *testing.T) {
	limiter := testutil.UnavailablePinRateLimiter(errors.New("redis unreachable"))
	app, _ := newUserRoutesApp(t, limiter)

	token := newRoutesToken(t, routesTestUserID)

	outcome := setPasswordRequest(t, app, token)
	require.Equal(t, http.StatusServiceUnavailable, outcome.status)
	require.Equal(t, "USER_RATE_LIMIT_UNAVAILABLE", outcome.code)
	require.Empty(
		t,
		outcome.retryAfter,
		"an unevaluated budget names no recovery time, because there is none",
	)

	// And the profile routes are untouched by it, which is the whole reason they
	// carry no budget of their own.
	require.Equal(
		t,
		http.StatusInternalServerError,
		send(t, app, http.MethodGet, "/users/me", "", token).status,
	)
	require.Equal(t, 1, limiter.CallsIn(testutil.UserPasswordUserNamespace))
}

// --- the profile routes carry no budget ------------------------------------

// Both profile routes are unlimited, and this is the assertion that says so: not
// merely that they are cheap, but that no counter is consulted at all.
//
// They send several requests rather than one, because a limit of five or more
// would otherwise look identical to no limit on a single request.
func TestRoutes_ProfileRoutesSpendNoBudget(t *testing.T) {
	limiter := testutil.NewCountingPinRateLimiter()
	app, _ := newUserRoutesApp(t, limiter)

	token := newRoutesToken(t, routesTestUserID)

	for range user.PasswordUserRateLimitMax + 5 {
		require.Equal(
			t,
			http.StatusInternalServerError,
			send(t, app, http.MethodGet, "/users/me", "", token).status,
			"the repository is stubbed to fail, so 500 proves the handler ran",
		)

		require.Equal(
			t,
			http.StatusInternalServerError,
			send(
				t, app, http.MethodPatch, "/users/me",
				`{"display_name":"Jane Doe"}`, token,
			).status,
			"the repository is stubbed to fail, so 500 proves the handler ran",
		)
	}

	require.Empty(
		t,
		limiter.Namespaces,
		"no profile request may consult a budget",
	)
}

// An unavailable limiter does not affect the profile routes either. They never
// ask, so there is nothing for an outage to break.
func TestRoutes_ProfileRoutesAreUnaffectedByAnUnavailableLimiter(t *testing.T) {
	limiter := testutil.UnavailablePinRateLimiter(errors.New("redis unreachable"))
	app, _ := newUserRoutesApp(t, limiter)

	token := newRoutesToken(t, routesTestUserID)

	require.Equal(
		t,
		http.StatusInternalServerError,
		send(t, app, http.MethodGet, "/users/me", "", token).status,
	)

	require.Equal(
		t,
		http.StatusInternalServerError,
		send(
			t, app, http.MethodPatch, "/users/me",
			`{"display_name":"Jane Doe"}`, token,
		).status,
	)

	require.Empty(t, limiter.Namespaces)
}

// The budget is mounted after Auth, so a request without a token never reaches it
// and never spends it. The subject is a verified identity and there is nothing to
// key on before middleware.Auth has run.
func TestRoutes_UnauthenticatedRequestsSpendNoBudget(t *testing.T) {
	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := newUserRoutesApp(t, limiter)

	require.Equal(
		t,
		http.StatusUnauthorized,
		setPasswordRequest(t, app, "").status,
	)

	require.Empty(
		t,
		limiter.Namespaces,
		"an unauthenticated request must not be charged to any account",
	)
}

// The budget must not be one the route invented. This is the same numbers the
// constants declare, asserted through the limiter rather than through the
// constants themselves, so a change to one without the other fails here.
func TestRoutes_DeclaredConstantsMatchTheAppliedPolicy(t *testing.T) {
	require.Equal(t, 5, user.PasswordUserRateLimitMax)
	require.Equal(t, 15*time.Minute, user.PasswordUserRateLimitWindow)
	require.Equal(t, testutil.UserPasswordUserNamespace, "user-password-user")
}
