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

	login "github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	logintestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/login/generated"
)

// The budgets guarding manual login, driven through HTTP.
//
// A request crosses two budgets before an authentication is attempted: the client
// address, spent by middleware in front of the handler, and the email address,
// spent by the service before the lookup. Every test below states what both do,
// because a test that arranged only one would be asserting on whichever was
// consulted first.
//
// The limiter here is the recording fake rather than Redis, so these assert the
// wiring: which namespace each budget is spent under, which subject, and what the
// refusal looks like on the wire. The counter itself is proven against a real
// Redis in auth_login_ip_rate_limit_test.go and in ratelimit_redis_test.go.

type loginResponse struct {
	status     int
	code       string
	retryAfter string
}

// createLoginAccount creates a user holding a real Argon2id credential, so a
// login fails on the password rather than on the lookup.
func createLoginAccount(t *testing.T, email string, password string) uuid.UUID {
	t.Helper()

	db := logintestdb.New(testPool)

	userID, err := db.CreateLoginUser(
		context.Background(),
		logintestdb.CreateLoginUserParams{
			Email:       email,
			DisplayName: "Login User",
		},
	)
	require.NoError(t, err)

	passwordHash, err := security.NewPasswordHasher().Hash(password)
	require.NoError(t, err)

	require.NoError(
		t,
		db.CreatePasswordCredential(
			context.Background(),
			logintestdb.CreatePasswordCredentialParams{
				UserID:       userID,
				PasswordHash: passwordHash,
			},
		),
	)

	return userID
}

func postLogin(
	t *testing.T,
	app *fiber.App,
	email string,
	password string,
) loginResponse {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/login",
		strings.NewReader(
			`{"email":"`+email+`","password":"`+password+`"}`,
		),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 60 * time.Second})
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

// decodeLoginError reads the error envelope off a refused response.
func decodeLoginError(t *testing.T, resp *http.Response, result *loginResponse) error {
	t.Helper()

	var body registerAPIError

	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}

	result.code = body.Error.Code

	return nil
}

// An exhausted per-address budget is a 429 with the feature's own code, and it
// issues nothing: no session row exists afterwards.
//
// The limiter enforces the policy it is given rather than answering from a
// fixed verdict, so the budget is genuinely spent here rather than declared
// spent. The per-address policy is the smaller of the two, so the eleven requests
// below leave the per-client budget well short of its own limit and it cannot be
// what is answering.
func TestLoginAPI_EmailFailureLimit_ExceededReturns429(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	const (
		email    = "login-email-limit@example.com"
		password = "correct-password"
	)

	userID := createLoginAccount(t, email, password)

	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	for attempt := range login.LoginEmailFailureLimit {
		result := postLogin(t, app, email, "wrong-password")

		require.Equal(
			t,
			http.StatusUnauthorized,
			result.status,
			"attempt %d should be within budget, got %s", attempt, result.code,
		)
		require.Equal(t, login.CodeInvalidCredentials, result.code)
	}

	result := postLogin(t, app, email, "wrong-password")

	require.Equal(t, http.StatusTooManyRequests, result.status)
	require.Equal(
		t,
		login.CodeLoginRateLimitExceeded,
		result.code,
		"the per-address budget allows %d failures in a window",
		login.LoginEmailFailureLimit,
	)

	sessionCount, err := db.CountUserSessions(ctx, userID)
	require.NoError(t, err)
	require.Zero(t, sessionCount)

	// The budget is the normalized address, not the submitted spelling, and it
	// is the service's namespace rather than the route's.
	subjects := limiter.SubjectsIn(testutil.LoginEmailNamespace)
	require.NotEmpty(t, subjects)

	for _, subject := range subjects {
		require.Equal(t, email, subject)
	}

	require.Len(
		t,
		limiter.SubjectsIn(testutil.LoginIPNamespace),
		login.LoginEmailFailureLimit+1,
		"every request also spent the client address budget",
	)
}

// The refused response carries a wait a client can act on, and it is derived from
// the counter rather than from the configured window. A refused request spends
// budget the window already had, so the wait can only hold steady or fall — a
// wait that climbed would let a refused caller hold the counter open.
func TestLoginAPI_EmailFailureLimit_ExceededCarriesRetryAfter(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	const email = "login-retry-after@example.com"

	createLoginAccount(t, email, "correct-password")

	limiter := testutil.NewCountingPinRateLimiter().
		AllowNamespace(testutil.LoginEmailNamespace, testutil.NamespaceOutcome{
			Allowed:    false,
			RetryAfter: 61 * time.Second,
		})

	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	var waits []int

	for range 3 {
		result := postLogin(t, app, email, "wrong-password")

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
			int(login.LoginEmailFailureWindow.Seconds()),
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

// One address's guesses must not spend another's. An attacker spreading one
// password list across many addresses is exactly the case this budget exists for,
// and it is only a real ceiling while one address's spend is one address's.
func TestLoginAPI_EmailFailureLimit_OneAddressDoesNotSpendAnother(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	const (
		target    = "login-neighbour@example.com"
		neighbour = "login-neighbour-other@example.com"
		password  = "correct-password"
	)

	userID := createLoginAccount(t, target, password)
	neighbourID := createLoginAccount(t, neighbour, password)

	require.NotEqual(t, userID, neighbourID)

	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	for attempt := range login.LoginEmailFailureLimit {
		result := postLogin(t, app, target, "wrong-password")

		require.Equal(
			t,
			http.StatusUnauthorized,
			result.status,
			"attempt %d should be within budget, got %s", attempt, result.code,
		)
	}

	// The budget is spent, so the same address is now refused.
	require.Equal(
		t,
		http.StatusTooManyRequests,
		postLogin(t, app, target, "wrong-password").status,
	)

	// A different address is untouched. Both requests come from the same client
	// address, because app.Test presents one peer, so nothing but the per-address
	// budget can be telling them apart.
	result := postLogin(t, app, neighbour, "wrong-password")

	require.Equal(
		t,
		http.StatusUnauthorized,
		result.status,
		"one address's guesses spent another's budget: %s", result.code,
	)
	require.Equal(t, login.CodeInvalidCredentials, result.code)

	// And the spent address is still spent, so nothing was quietly restored.
	require.Equal(
		t,
		http.StatusTooManyRequests,
		postLogin(t, app, target, "wrong-password").status,
	)
}

// Two spellings of one address are one account, so they share one budget.
//
// The lookup compares exactly against the stored form, so a budget keyed on the
// raw submission would hand an attacker `Alice@Example.com` and
// `alice@example.com` as two independent allowances for the same account.
func TestLoginAPI_EmailFailureLimit_IsKeyedOnTheNormalizedAddress(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	const email = "login-normalized@example.com"

	createLoginAccount(t, email, "correct-password")

	spellings := []string{
		"Login-Normalized@Example.COM",
		"LOGIN-NORMALIZED@EXAMPLE.COM",
		"login-normalized@example.com",
		"LoGiN-NoRmAlIzEd@ExAmPlE.cOm",
		"LOGIN-normalized@example.COM",
	}

	// Every spelling is submitted exactly as many times as the budget allows, so
	// the request after them is refused only if all of them drew on one budget.
	// A budget keyed on the raw submission would treat each spelling as its own
	// account and none of them would ever run out.
	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	submitted := make([]string, 0, login.LoginEmailFailureLimit)

	for range login.LoginEmailFailureLimit {
		submitted = append(submitted, spellings[len(submitted)%len(spellings)])
	}

	for index, attempt := range submitted {
		result := postLogin(t, app, attempt, "wrong-password")

		require.Equal(
			t,
			http.StatusUnauthorized,
			result.status,
			"attempt %d should be within budget, got %s", index, result.code,
		)
	}

	result := postLogin(t, app, email, "wrong-password")

	require.Equal(
		t,
		http.StatusTooManyRequests,
		result.status,
		"one address spelled several ways was given several budgets",
	)

	subjects := limiter.SubjectsIn(testutil.LoginEmailNamespace)
	require.Len(t, subjects, len(submitted)+1)

	for _, subject := range subjects {
		require.Equal(
			t,
			email,
			subject,
			"the budget must be the canonical address, not the submitted spelling",
		)
	}
}

// A successful authentication hands back the charge it was given optimistically,
// so signing in does not spend a failure budget. Without the release, a legitimate
// user who signed in a few times would be unable to sign in at all.
func TestLoginAPI_EmailFailureLimit_ASuccessfulLoginRestoresTheBudget(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	const (
		email    = "login-restored@example.com"
		password = "correct-password"
	)

	userID := createLoginAccount(t, email, password)

	// One failure short of the ceiling, so the address is still able to
	// authenticate and the effect of the release is visible as a refusal not
	// happening rather than as a request succeeding.
	limiter := testutil.NewEnforcingPinRateLimiter()
	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	for range login.LoginEmailFailureLimit - 1 {
		require.Equal(
			t,
			http.StatusUnauthorized,
			postLogin(t, app, email, "wrong-password").status,
			"the failures before the last should be within budget",
		)
	}

	before := limiter.ChargeFor(testutil.LoginEmailNamespace)
	require.Equal(t, int64(login.LoginEmailFailureLimit-1), before)

	result := postLogin(t, app, email, password)
	require.Equal(t, http.StatusOK, result.status, result.code)

	require.Equal(
		t,
		before,
		limiter.ChargeFor(testutil.LoginEmailNamespace),
		"a successful login must leave the budget where it started",
	)

	// The allowance is whole again, so a further failure costs one of it rather
	// than being refused on top of the ones already spent.
	require.Equal(
		t,
		http.StatusUnauthorized,
		postLogin(t, app, email, "wrong-password").status,
	)

	require.Equal(
		t,
		before+1,
		limiter.ChargeFor(testutil.LoginEmailNamespace),
	)

	sessionCount, err := db.CountUserSessions(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(1), sessionCount)
}

// A limiter that cannot answer is a refusal, not permission.
//
// The whole limiter is failing here, so the client budget is the layer that meets
// the request first and the code reported is that layer's. Credentials are never
// tried and nothing is issued.
func TestLoginAPI_RateLimit_UnavailableReturns503(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	const (
		email    = "login-unavailable@example.com"
		password = "correct-password"
	)

	userID := createLoginAccount(t, email, password)

	limiter := testutil.UnavailablePinRateLimiter(
		errors.New("dial tcp: connection refused"),
	)

	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	// Right password included. An outage must not become a way to have credentials
	// checked, and must not become a way to be told they are wrong either.
	result := postLogin(t, app, email, password)

	require.Equal(t, http.StatusServiceUnavailable, result.status)
	require.Equal(t, "IP_RATE_LIMIT_UNAVAILABLE", result.code)

	sessionCount, err := db.CountUserSessions(ctx, userID)
	require.NoError(t, err)
	require.Zero(t, sessionCount, "no session may be issued when the budget is unknown")
}

// The per-address budget fails the same way when only it is the one that cannot
// answer, which is the state a Redis outage looks like once the client budget has
// been spent within its window. The code is the feature's own, because the caller
// is being refused over an account and not over a network address.
func TestLoginAPI_EmailFailureLimit_UnavailableReturns503(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	const (
		email    = "login-email-unavailable@example.com"
		password = "correct-password"
	)

	userID := createLoginAccount(t, email, password)

	limiter := testutil.UnavailablePinRateLimiter(
		errors.New("dial tcp: connection refused"),
	).AllowNamespace(testutil.LoginIPNamespace, testutil.NamespaceOutcome{
		Allowed: true,
	})

	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	result := postLogin(t, app, email, password)

	require.Equal(t, http.StatusServiceUnavailable, result.status)
	require.Equal(t, login.CodeLoginRateLimitUnavailable, result.code)

	// A 503 names no recovery time, because there is none to name. Fabricating a
	// Retry-After would tell a client to come back at a moment nobody chose.
	require.Empty(
		t,
		result.retryAfter,
		"an outage has no window to wait out",
	)

	sessionCount, err := db.CountUserSessions(ctx, userID)
	require.NoError(t, err)
	require.Zero(t, sessionCount)
}

// An exhausted client budget stops the request before the handler, so the per
// address budget is never spent. A refused request must not cost the caller a
// second unit of the other budget, or a single flood would lock the account out
// on top of blocking the network.
func TestLoginAPI_IPRateLimit_ExceededReturns429(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	const (
		email    = "login-ip-limit@example.com"
		password = "correct-password"
	)

	userID := createLoginAccount(t, email, password)

	limiter := testutil.NewCountingPinRateLimiter().
		AllowNamespace(testutil.LoginIPNamespace, testutil.NamespaceOutcome{
			Allowed: false,
		})

	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	result := postLogin(t, app, email, password)

	require.Equal(t, http.StatusTooManyRequests, result.status)
	require.Equal(t, "IP_RATE_LIMIT_EXCEEDED", result.code)
	require.NotEmpty(t, result.retryAfter)

	require.Empty(
		t,
		limiter.SubjectsIn(testutil.LoginEmailNamespace),
		"a request the client budget refused must not spend a second budget",
	)

	sessionCount, err := db.CountUserSessions(ctx, userID)
	require.NoError(t, err)
	require.Zero(t, sessionCount)
}

// The two budgets are separate counters, not one read twice. A successful login
// spends the address budget back to zero and leaves the client budget alone, which
// is only observable if they are distinct.
func TestLoginAPI_RateLimit_TheTwoBudgetsAreSeparate(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	const (
		email    = "login-separate@example.com"
		password = "correct-password"
	)

	createLoginAccount(t, email, password)

	limiter := testutil.NewCountingPinRateLimiter()
	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	require.Equal(
		t,
		http.StatusOK,
		postLogin(t, app, email, password).status,
	)

	require.Zero(t, limiter.ChargeFor(testutil.LoginEmailNamespace))
	require.Equal(
		t,
		int64(1),
		limiter.ChargeFor(testutil.LoginIPNamespace),
		"a successful login still cost the client one request of its budget",
	)
}
