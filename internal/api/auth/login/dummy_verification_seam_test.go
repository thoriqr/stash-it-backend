package login_test

import (
	"context"
	"errors"
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

// Whether an unknown address pays for password work is the account-enumeration
// defence for manual login. It used to be asserted only by comparing elapsed
// times, which measures Argon2id and the machine running it as much as it
// measures the code: the two paths were observed within a factor of 1.0 of each
// other, but under a loaded parallel suite the single-path minimum drifted by
// more than the threshold it was compared against.
//
// These tests assert the claim directly instead. The service asks a
// PasswordVerifier for its work, so a test can watch which of Verify and
// DummyVerify it asked for, and the timing assertion becomes a secondary
// end-to-end signal rather than the only thing standing between a caller and a
// list of which addresses have accounts.

// recordingVerifier records which password operation the service performed and
// delegates the actual behaviour to a real hasher, so the assertions below are
// about the service's choice rather than about a stub standing in for one.
type recordingVerifier struct {
	inner *security.PasswordHasher

	verifyCalls      int
	dummyVerifyCalls int

	// dummyErr, when set, is what DummyVerify reports. It models a limiter that
	// could not grant capacity without pinning a specific failure.
	dummyErr error
}

func (r *recordingVerifier) Verify(
	ctx context.Context,
	password string,
	encodedHash string,
) (security.PasswordVerificationResult, error) {
	r.verifyCalls++

	return r.inner.Verify(ctx, password, encodedHash)
}

func (r *recordingVerifier) DummyVerify(
	ctx context.Context,
	password string,
) error {
	r.dummyVerifyCalls++

	if r.dummyErr != nil {
		return r.dummyErr
	}

	return r.inner.DummyVerify(ctx, password)
}

func (r *recordingVerifier) Wait() time.Duration {
	return r.inner.Wait()
}

// newServiceWithRecordingVerifier builds a service whose password work is
// observable, and returns the recorder alongside it.
func newServiceWithRecordingVerifier(
	t *testing.T,
	verifier *recordingVerifier,
) (
	*login.Service,
	*loginmocks.MockRepository,
	*sessionmocks.MockSessionCreator,
	*testutil.CountingPinRateLimiter,
) {
	t.Helper()

	ctrl := gomock.NewController(t)

	repository := loginmocks.NewMockRepository(ctrl)
	sessionCreator := sessionmocks.NewMockSessionCreator(ctrl)
	rateLimiter := testutil.NewCountingPinRateLimiter()

	service := login.NewService(
		repository,
		sessionCreator,
		nil,
		&mockGoogleTokenVerifier{},
		verifier,
		security.NewAccessTokenGenerator([]byte("test-secret")),
		rateLimiter,
	)

	return service, repository, sessionCreator, rateLimiter
}

// expectUnknownAccount arranges for an address no account is stored for.
func expectUnknownAccount(
	repository *loginmocks.MockRepository,
	email string,
) {
	repository.EXPECT().
		GetUserForLogin(gomock.Any(), email).
		Return(
			logindb.GetUserForLoginRow{},
			apperror.UnauthorizedWith(
				login.CodeInvalidCredentials,
				"invalid email or password",
				nil,
			),
		).
		AnyTimes()
}

// An address with no account must still have a password derivation performed
// against it. Without this the endpoint returns before any password work, while
// a wrong password on a real account pays for a full Argon2id derivation, and
// the gap between the two is enough to tell an observer which addresses exist.
func TestService_LoginManual_AnUnknownAddressPerformsADummyDerivation(t *testing.T) {
	const unknown = "nobody@example.com"

	verifier := &recordingVerifier{
		inner: security.NewPasswordHasher(testutil.NewPasswordWorkLimiter()),
	}

	service, repository, _, _ := newServiceWithRecordingVerifier(t, verifier)

	expectUnknownAccount(repository, unknown)

	_, err := service.LoginManual(
		context.Background(),
		unknown,
		wrongPassword,
		session.SessionMetadata{},
	)

	require.Error(t, err)
	require.Equal(
		t,
		login.CodeInvalidCredentials,
		apperror.FromError(err).Code,
		"the response is unchanged: a probe learns nothing either way",
	)

	require.Equal(
		t,
		1,
		verifier.dummyVerifyCalls,
		"an unknown address returned without doing password work, so which "+
			"addresses exist can be read off how long the answer takes",
	)

	require.Zero(
		t,
		verifier.verifyCalls,
		"there is no stored hash for an unknown address, so Verify must not be "+
			"what ran",
	)
}

// The same call on a real account must take the other branch. Asserting both
// directions is what makes this a statement about which path the code takes
// rather than about the counter simply having been incremented somewhere.
func TestService_LoginManual_AKnownAccountVerifiesAndDoesNotRunTheDummyPath(t *testing.T) {
	const email = "alice@example.com"

	limiter := testutil.NewPasswordWorkLimiter()

	passwordHash, err := security.NewPasswordHasher(limiter).
		Hash(context.Background(), "correct-password")
	require.NoError(t, err)

	verifier := &recordingVerifier{
		inner: security.NewPasswordHasher(limiter),
	}

	service, repository, sessionCreator, _ :=
		newServiceWithRecordingVerifier(t, verifier)

	userID := uuid.New()

	repository.EXPECT().
		GetUserForLogin(gomock.Any(), email).
		Return(logindb.GetUserForLoginRow{
			ID:           userID,
			Email:        email,
			DisplayName:  "Alice",
			PasswordHash: passwordHash,
		}, nil).
		AnyTimes()

	sessionCreator.EXPECT().
		CreateSession(gomock.Any(), userID, gomock.Any()).
		Return(session.CreateSessionResult{
			Session:      sessiondb.Session{ID: uuid.New(), UserID: userID},
			RefreshToken: "refresh-token",
		}, nil).
		AnyTimes()

	_, err = service.LoginManual(
		context.Background(),
		email,
		wrongPassword,
		session.SessionMetadata{},
	)

	require.Error(t, err)
	require.Equal(t, login.CodeInvalidCredentials, apperror.FromError(err).Code)

	require.Equal(t, 1, verifier.verifyCalls)
	require.Zero(
		t,
		verifier.dummyVerifyCalls,
		"a real account must not also pay for the dummy derivation: doing both "+
			"would double the cost of every legitimate failed login",
	)
}

// The dummy path must be reached exactly once per request rather than skipped,
// retried or short-circuited by an early return. Repeating it would make an
// unknown address slower than a known one, which is the same disclosure in the
// other direction.
func TestService_LoginManual_TheDummyDerivationIsPerformedOncePerRequest(t *testing.T) {
	const unknown = "nobody@example.com"
	const attempts = 4

	verifier := &recordingVerifier{
		inner: security.NewPasswordHasher(limiterArg(t, 2)),
	}

	service, repository, _, _ := newServiceWithRecordingVerifier(t, verifier)

	expectUnknownAccount(repository, unknown)

	for range attempts {
		_, err := service.LoginManual(
			context.Background(),
			unknown,
			wrongPassword,
			session.SessionMetadata{},
		)

		require.Error(t, err)
	}

	require.Equal(t, attempts, verifier.dummyVerifyCalls)
}

// A dummy derivation that fails is reported rather than swallowed. The
// repository has already failed by this point, so its error is sitting there
// ready to be returned, and returning it would tell a probing caller that their
// guess had been checked when nothing checked anything.
func TestService_LoginManual_ADummyDerivationFailureIsNotReportedAsCredentials(t *testing.T) {
	const unknown = "nobody@example.com"

	verifier := &recordingVerifier{
		inner:    security.NewPasswordHasher(testutil.NewPasswordWorkLimiter()),
		dummyErr: errors.New("derivation unavailable"),
	}

	service, repository, _, _ := newServiceWithRecordingVerifier(t, verifier)

	expectUnknownAccount(repository, unknown)

	_, err := service.LoginManual(
		context.Background(),
		unknown,
		wrongPassword,
		session.SessionMetadata{},
	)

	require.Error(t, err)
	require.Equal(t, 1, verifier.dummyVerifyCalls)

	require.NotEqual(
		t,
		login.CodeInvalidCredentials,
		apperror.FromError(err).Code,
		"the password was never checked, so reporting invalid credentials "+
			"would be a lie the caller acts on",
	)
}

// limiterArg keeps the concurrency argument out of the test bodies above.
func limiterArg(t *testing.T, maxConcurrent int) *security.PasswordWorkLimiter {
	t.Helper()

	limiter, err := security.NewPasswordWorkLimiter(maxConcurrent, time.Second)
	require.NoError(t, err)

	return limiter
}
