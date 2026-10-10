package integration_test

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	login "github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	logintestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/login/generated"
)

// What a client actually receives when the server has no password-work capacity.
//
// The service tests prove the error a feature builds; the httpx tests prove the
// handler emits a header for an error that carries a wait. Neither shows the two
// joined, and this is the only place they are joined: it drives the real router,
// the real handler and the real limiter, and asserts what is on the wire.
//
// The single slot is taken before the request is made, so no Argon2id derivation
// runs here at all. That is what makes it cheap and, more to the point, what makes
// it deterministic: a test that waited for a real derivation to occupy the slot
// would be measuring this machine's Argon2id speed rather than the behaviour.

const passwordWorkWait = 800 * time.Millisecond

// saturatedApp builds the shared test app over a limiter whose only slot is
// already held, plus the release that gives it back.
func saturatedApp(
	t *testing.T,
) (*fiber.App, func()) {
	t.Helper()

	limiter, err := security.NewPasswordWorkLimiter(1, passwordWorkWait)
	require.NoError(t, err)

	release, err := limiter.Acquire(context.Background())
	require.NoError(t, err)

	app, _ := testutil.NewAppWithLimiters(
		testPool,
		testutil.NewPermissivePinRateLimiter(),
		limiter,
	)

	return app, release
}

func TestLoginAPI_PasswordWorkCapacityIs503WithRetryAfter(t *testing.T) {
	ctx := context.Background()
	require.NoError(t, logintestdb.New(testPool).TruncateLoginData(ctx))

	app, release := saturatedApp(t)
	defer release()

	const email = "http-capacity@example.com"

	// A real account, so the request reaches Verify rather than the
	// unknown-account path. Both are covered at the service level; this is about
	// what reaches the wire.
	createLoginAccount(t, email, "correct-password")

	response := postLogin(t, app, email, "wrong-password")

	require.Equal(t, http.StatusServiceUnavailable, response.status, response.code)
	require.Equal(t, login.CodePasswordWorkUnavailable, response.code)

	// The header is the point of the whole feature: without it a client told the
	// server is busy has nothing to act on and retries straight back in.
	retryAfter := response.retryAfter
	require.NotEmpty(t, retryAfter, "a capacity refusal must say when to come back")

	seconds, err := strconv.Atoi(retryAfter)
	require.NoError(t, err, "Retry-After must be a whole number of seconds")
	require.Positive(t, seconds)

	// The header's contract is "not before", so it rounds the configured wait up
	// to the next whole second rather than down. Comparing against the raw wait
	// would assert 1 <= 0 for a sub-second one, which is nonsense; the value that
	// is actually promised is the ceiling.
	//
	// This is the assertion that would catch an unbounded value: the header is
	// pinned to the configured wait exactly, so it can never send a client away
	// for longer than the server itself asked it to wait.
	require.Equal(
		t,
		int(math.Ceil(passwordWorkWait.Seconds())),
		seconds,
		"Retry-After must be the configured wait, rounded up to whole seconds",
	)
}

// The same response must not be a rate limit, and must not claim the credentials
// were checked.
//
// Both would be actionable lies: a client told its password was wrong would go
// and choose a different one, and a client told it had spent a budget would back
// off for a window that does not exist.
func TestLoginAPI_PasswordWorkCapacityIsNotInvalidCredentialsOrARateLimit(t *testing.T) {
	ctx := context.Background()
	require.NoError(t, logintestdb.New(testPool).TruncateLoginData(ctx))

	app, release := saturatedApp(t)
	defer release()

	const email = "http-not-a-limit@example.com"

	createLoginAccount(t, email, "correct-password")

	response := postLogin(t, app, email, "wrong-password")

	require.NotEqual(t, login.CodeInvalidCredentials, response.code)
	require.NotEqual(t, login.CodeLoginRateLimitExceeded, response.code)
	require.NotEqual(t, http.StatusUnauthorized, response.status)
	require.NotEqual(t, http.StatusTooManyRequests, response.status)

	// The message itself is asserted at the service level in both features. What
	// is under test here is the wire contract — status, code and header — and
	// decoding the body again for one string would duplicate the shared helper.
}

// Capacity returning makes the identical request succeed, which is what makes the
// retry a real instruction rather than a dead end.
//
// The recovery path costs one real derivation, which is the point: proving the
// same request is refused and then accepted is what shows the refusal is
// transient rather than a lockout the client cannot escape.
func TestLoginAPI_PasswordWorkCapacityRecoversOnceASlotIsFree(t *testing.T) {
	ctx := context.Background()
	require.NoError(t, logintestdb.New(testPool).TruncateLoginData(ctx))

	app, release := saturatedApp(t)

	const (
		email    = "http-recovers@example.com"
		password = "correct-password"
	)

	createLoginAccount(t, email, password)

	refused := postLogin(t, app, email, password)
	require.Equal(
		t,
		login.CodePasswordWorkUnavailable,
		refused.code,
		"the password was never checked, so this is not a credential answer",
	)

	release()

	accepted := postLogin(t, app, email, password)
	require.Equal(t, http.StatusOK, accepted.status, accepted.code)
}
