package integration_test

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	login "github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	logintestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/login/generated"
	passwordresetdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/password_reset/generated"
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

// Password reset finalization performs the same kind of Argon2id derivation, on
// the same shared limiter, so it refuses the same way.
//
// The state assertions below are the reason this test exists rather than a
// copy of the login one. A capacity refusal on this route happens before the
// finalization transaction opens, so the continuation must be left unconsumed and
// the pending reset must be left pending — otherwise a momentarily busy server
// would turn a user who proved control of their mailbox into someone who can
// never finish the reset.

// seedCredentialAndSession gives an existing account a real credential and an
// active session, so a finalization that ran would change both.
//
// It takes the user id rather than creating the account, because the flow has to
// be opened first: a reset can only be opened for an address an account already
// holds, so the account exists before this is useful and is created by the flow.
func seedCredentialAndSession(
	t *testing.T,
	userID uuid.UUID,
	password string,
) {
	t.Helper()

	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	hash, err := testutil.NewPasswordHasher().Hash(ctx, password)
	require.NoError(t, err)

	require.NoError(
		t,
		db.CreatePasswordCredentialForUser(
			ctx,
			passwordresetdbtest.CreatePasswordCredentialForUserParams{
				UserID:       userID,
				PasswordHash: hash,
			},
		),
	)

	_, err = db.CreateSessionForUser(ctx, userID)
	require.NoError(t, err)
}

func TestPasswordResetFinalizeAPI_PasswordWorkCapacityIs503(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const email = "http-reset-capacity@example.com"
	const previousPassword = "the-password-in-force"

	// The flow is opened through the shared app, which has its own limiter and its
	// own capacity, so arranging the fixture cannot occupy the slot this test is
	// about.
	userID, verificationID, issuedPIN := startPasswordResetWithPIN(t, email)

	seedCredentialAndSession(t, userID, previousPassword)

	minted := verifyPasswordResetPIN(verificationID, issuedPIN)
	require.NoError(t, minted.err)
	require.Equal(t, http.StatusOK, minted.status, minted.code)
	require.NotEmpty(t, minted.token)

	app, release := saturatedApp(t)

	response := postResetFinalizeFrom(
		t,
		app,
		minted.token,
		"a-brand-new-password",
		"",
	)

	require.Equal(t, http.StatusServiceUnavailable, response.status, response.code)
	require.Equal(
		t,
		passwordreset.CodePasswordWorkUnavailable,
		response.code,
	)

	// The header is the point of the whole feature: without it a client told the
	// server is busy has nothing to act on and retries straight back in.
	retryAfter := response.retryAfter
	require.NotEmpty(t, retryAfter, "a capacity refusal must say when to come back")

	seconds, err := strconv.Atoi(retryAfter)
	require.NoError(t, err, "Retry-After must be a whole number of seconds")
	require.Positive(t, seconds)

	// The header's contract is "not before", so it rounds the configured wait up to
	// the next whole second rather than down, and never further.
	require.Equal(
		t,
		int(math.Ceil(passwordWorkWait.Seconds())),
		seconds,
		"Retry-After must be the configured wait, rounded up to whole seconds",
	)

	// Nothing was decided. The password was never hashed, so no credential was
	// written, the continuation was not spent, the reset did not complete and the
	// session was not revoked.
	state, err := db.GetPasswordResetFinalState(ctx, email)
	require.NoError(t, err)

	require.False(
		t,
		state.ConsumedAt.Valid,
		"a capacity refusal must leave the continuation unconsumed",
	)
	require.Equal(
		t,
		string(passwordreset.PendingPasswordResetPending),
		state.PasswordResetStatus,
		"a capacity refusal must leave the reset pending",
	)
	require.Equal(
		t,
		int64(1),
		state.ActiveSessionCount,
		"a capacity refusal must not revoke the user's sessions",
	)

	stillOld, err := testutil.NewPasswordHasher().Verify(
		ctx,
		previousPassword,
		state.PasswordHash,
	)
	require.NoError(t, err)
	require.True(
		t,
		stillOld.Match,
		"a capacity refusal must leave the previous password in force",
	)

	// And the capability really was intact: the identical request succeeds once a
	// slot is free. Without this the assertions above would also hold if the
	// refusal had consumed something unusable.
	release()

	accepted := postResetFinalizeFrom(
		t,
		app,
		minted.token,
		"a-brand-new-password",
		"",
	)
	require.Equal(t, http.StatusOK, accepted.status, accepted.code)

	finalState, err := db.GetPasswordResetFinalState(ctx, email)
	require.NoError(t, err)

	require.True(t, finalState.ConsumedAt.Valid)
	require.Equal(
		t,
		string(passwordreset.PendingPasswordResetCompleted),
		finalState.PasswordResetStatus,
	)
	require.Zero(t, finalState.ActiveSessionCount)

	changed, err := testutil.NewPasswordHasher().Verify(
		ctx,
		"a-brand-new-password",
		finalState.PasswordHash,
	)
	require.NoError(t, err)
	require.True(t, changed.Match, "the new password must be in force once capacity returned")
}
