package login_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	logindb "github.com/thoriqr/stash-it-backend/internal/api/auth/login/generated"
	loginmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/login/mocks"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	sessionmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/session/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
)

// What happens when every slot for expensive password work is already occupied.
//
// Nothing here waits for a real derivation to finish or measures how long one
// takes. The single slot is held from outside the request, so the refusal is
// certain and instant and what is under test is the shape of the answer rather
// than anything about Argon2id.

// saturatedHasher returns a hasher whose only slot is already taken, plus the
// release that gives it back.
//
// Holding the slot from the test is what makes these deterministic: the service
// is certain to find no capacity without anything having to be raced.
func saturatedHasher(
	t *testing.T,
) (*security.PasswordHasher, *security.PasswordWorkLimiter, func()) {
	t.Helper()

	limiter, err := security.NewPasswordWorkLimiter(1, time.Second)
	require.NoError(t, err)

	release, err := limiter.Acquire(context.Background())
	require.NoError(t, err)

	return security.NewPasswordHasher(limiter), limiter, release
}

// newServiceWithSaturatedHasher builds a service over a hasher that has no
// capacity, plus the limiter it is held by so a test can mint a real credential
// before the slot is taken and let it go afterwards.
func newServiceWithSaturatedHasher(
	t *testing.T,
) (
	*login.Service,
	*loginmocks.MockRepository,
	*sessionmocks.MockSessionCreator,
	*testutil.CountingPinRateLimiter,
	*security.PasswordWorkLimiter,
	func(),
) {
	t.Helper()

	hasher, limiter, release := saturatedHasher(t)

	ctrl := gomock.NewController(t)

	repository := loginmocks.NewMockRepository(ctrl)
	sessionCreator := sessionmocks.NewMockSessionCreator(ctrl)
	rateLimiter := testutil.NewCountingPinRateLimiter()

	service := login.NewService(
		repository,
		sessionCreator,
		nil,
		&mockGoogleTokenVerifier{},
		hasher,
		security.NewAccessTokenGenerator([]byte("test-secret")),
		rateLimiter,
	)

	return service, repository, sessionCreator, rateLimiter, limiter, release
}

// credentialWith builds a real Argon2id hash using the limiter directly, before
// any slot is held.
//
// It has to be a real one and it has to be minted this way: a hash produced
// through the saturated hasher would be empty, and an empty string is refused by
// the parser before the limiter is ever consulted — which would test the parse
// guard rather than the capacity bound.
func credentialWith(
	t *testing.T,
	limiter *security.PasswordWorkLimiter,
	password string,
) string {
	t.Helper()

	// A separate limiter, so building the credential cannot consume the one slot
	// the request is going to find busy.
	other, err := security.NewPasswordWorkLimiter(1, time.Second)
	require.NoError(t, err)

	hasher := security.NewPasswordHasher(other)

	release, err := other.Acquire(context.Background())
	require.NoError(t, err)
	release()

	encoded, err := hasher.Hash(context.Background(), password)
	require.NoError(t, err)

	return encoded
}

// A wrong password for a real account is refused with a 503 and a bounded
// Retry-After, not with the invalid-credentials answer the same request earns
// when capacity is free.
//
// The distinction is the point. The password was never checked, so reporting it
// as a bad password would be a lie the caller would act on — and it would be the
// same lie for every address, so it leaks nothing about which accounts exist
// while being wrong about every one of them.
func TestService_LoginManual_CapacityExhaustionIsNotInvalidCredentials(t *testing.T) {
	service, repository, _, _, limiter, release :=
		newServiceWithSaturatedHasher(t)
	defer release()

	const email = "capacity@example.com"

	repository.EXPECT().
		GetUserForLogin(gomock.Any(), email).
		Return(logindb.GetUserForLoginRow{
			ID:           uuid.New(),
			Email:        email,
			PasswordHash: credentialWith(t, limiter, "correct-password"),
		}, nil).
		AnyTimes()

	_, err := service.LoginManual(
		context.Background(),
		email,
		wrongPassword,
		session.SessionMetadata{},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)

	require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
	require.Equal(t, login.CodePasswordWorkUnavailable, appErr.Code)
	require.NotEqual(t, login.CodeInvalidCredentials, appErr.Code)

	// The caller is told when to come back, and the value is bounded by the wait
	// it was actually kept waiting for.
	require.NotNil(t, appErr.RetryAfterSeconds())
	require.Positive(t, *appErr.RetryAfterSeconds())
	require.LessOrEqual(t, *appErr.RetryAfterSeconds(), 1)
}

// The unknown-account path is refused the same way.
//
// It reaches DummyVerify rather than Verify, and it would be easy to leave that
// path reporting invalid credentials: the repository has already failed, so the
// original error is sitting there ready to be returned. Reporting it would tell
// a caller probing addresses that their guess had been checked when nothing
// checked anything.
func TestService_LoginManual_CapacityExhaustionOnTheUnknownAccountPathIs503(t *testing.T) {
	service, repository, _, _, _, release :=
		newServiceWithSaturatedHasher(t)
	defer release()

	const unknown = "nobody@example.com"

	repository.EXPECT().
		GetUserForLogin(gomock.Any(), unknown).
		Return(
			logindb.GetUserForLoginRow{},
			apperror.UnauthorizedWith(
				login.CodeInvalidCredentials,
				"invalid email or password",
				nil,
			),
		).
		AnyTimes()

	_, err := service.LoginManual(
		context.Background(),
		unknown,
		wrongPassword,
		session.SessionMetadata{},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)

	require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
	require.Equal(t, login.CodePasswordWorkUnavailable, appErr.Code)
	require.NotEqual(
		t,
		login.CodeInvalidCredentials,
		appErr.Code,
		"the password was never checked, so this is not a credential answer",
	)
	require.NotNil(t, appErr.RetryAfterSeconds())
}

// The refusal is not a rate limit, and the per-email budget still behaves.
//
// Nothing was spent against a window that does not exist, so this must not wear
// the rate limit's code or its status. What must still hold is the accounting
// that was already there: the attempt spends the failure budget it was charged,
// and nothing is given back because nothing authenticated.
func TestService_LoginManual_CapacityExhaustionIsNotTheEmailRateLimit(t *testing.T) {
	service, repository, _, rateLimiter, limiter, release :=
		newServiceWithSaturatedHasher(t)
	defer release()

	const email = "busy@example.com"

	repository.EXPECT().
		GetUserForLogin(gomock.Any(), email).
		Return(logindb.GetUserForLoginRow{
			ID:           uuid.New(),
			Email:        email,
			PasswordHash: credentialWith(t, limiter, "correct-password"),
		}, nil).
		AnyTimes()

	_, err := service.LoginManual(
		context.Background(),
		email,
		wrongPassword,
		session.SessionMetadata{},
	)

	appErr := apperror.FromError(err)

	require.NotEqual(t, login.CodeLoginRateLimitExceeded, appErr.Code)
	require.NotEqual(t, http.StatusTooManyRequests, appErr.Status)

	require.Zero(
		t,
		rateLimiter.ReleasesIn(testutil.LoginEmailNamespace),
		"nothing authenticated, so nothing may be given back",
	)
	require.Equal(
		t,
		int64(1),
		rateLimiter.ChargeFor(testutil.LoginEmailNamespace),
		"the attempt still spends the failure budget it was charged",
	)
}

// Capacity that comes back makes the very same request succeed, and the charge is
// then released as it always is on a successful authentication.
//
// This is what separates a transient refusal from a lockout, and it is the whole
// reason the response carries a Retry-After.
func TestService_LoginManual_LoginSucceedsOnceCapacityReturns(t *testing.T) {
	service, repository, sessionCreator, rateLimiter, limiter, release :=
		newServiceWithSaturatedHasher(t)

	const (
		email    = "returns@example.com"
		password = "correct-password"
	)

	userID := uuid.New()

	repository.EXPECT().
		GetUserForLogin(gomock.Any(), email).
		Return(logindb.GetUserForLoginRow{
			ID:           userID,
			Email:        email,
			PasswordHash: credentialWith(t, limiter, password),
		}, nil).
		AnyTimes()

	sessionCreator.EXPECT().
		CreateSession(gomock.Any(), userID, gomock.Any()).
		Return(session.CreateSessionResult{
			Session:      sessiondb.Session{ID: uuid.New(), UserID: userID},
			RefreshToken: "refresh-token",
		}, nil).
		AnyTimes()

	// Refused while the slot is held.
	_, err := service.LoginManual(
		context.Background(),
		email,
		password,
		session.SessionMetadata{},
	)

	require.Equal(
		t,
		login.CodePasswordWorkUnavailable,
		apperror.FromError(err).Code,
	)

	require.Equal(
		t,
		int64(1),
		rateLimiter.ChargeFor(testutil.LoginEmailNamespace),
		"the refused attempt spent its charge",
	)

	// Accepted once the slot is given back, and the charge handed straight back
	// because this one really did authenticate.
	release()

	result, err := service.LoginManual(
		context.Background(),
		email,
		password,
		session.SessionMetadata{},
	)

	require.NoError(t, err)
	require.NotEmpty(t, result.AccessToken)

	// Exactly one release: the successful login handed back its own charge and
	// nothing else. The refused attempt above keeps its own, which is right — it
	// never authenticated, so it is a failure exactly as a wrong password would
	// be. One charge for one real failure is what makes this number 1 rather
	// than 0 or 2.
	require.Equal(
		t,
		1,
		rateLimiter.ReleasesIn(testutil.LoginEmailNamespace),
		"a successful login releases exactly its own charge",
	)
	require.Equal(
		t,
		int64(1),
		rateLimiter.ChargeFor(testutil.LoginEmailNamespace),
		"only the refused attempt, which never authenticated, is still counted",
	)
}
