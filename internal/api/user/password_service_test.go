package user

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// credentialAbsent is what the repository answers when the account holds no
// credential. It is a conflict carrying a dedicated code rather than a not-found,
// and the whole create branch is built on that being recognised.
func credentialAbsent() error {
	return apperror.ConflictWith(
		CodePasswordCredentialNotFound,
		"this account has no password yet",
		nil,
	)
}

func setPasswordParams() SetPasswordParams {
	return SetPasswordParams{
		UserID:           testUserID,
		CurrentSessionID: testSessionID,
		CurrentPassword:  "current-password",
		NewPassword:      "new-password-123",
	}
}

// setCreateParams is the same request for an account holding no credential:
// new_password alone, because there is nothing for a current password to verify
// against.
func setCreateParams() SetPasswordParams {
	return SetPasswordParams{
		UserID:           testUserID,
		CurrentSessionID: testSessionID,
		NewPassword:      "new-password-123",
	}
}

// --- create branch ---------------------------------------------------------

// The create branch must not spend a derivation proving a current password it
// has no hash to check. DummyVerify exists for an unauthenticated login against
// an unknown address; neither condition holds here.
func TestSetPassword_CreateNeverVerifiesAPasswordItHasNoHashFor(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("", credentialAbsent())

	passwordHasher.EXPECT().
		Hash(ctx, "new-password-123").
		Return("new-hash", nil)

	repository.EXPECT().
		CreatePasswordCredential(ctx, testUserID, "new-hash").
		Return(nil)

	// Verify is never expected. gomock fails on an unexpected call, so a service
	// that reached for it would fail this test.
	result, err := service.SetPassword(ctx, setCreateParams())
	require.NoError(t, err)
	require.Equal(t, SetPasswordCreated, result)
}

// The stored value is the hash, never what was submitted.
func TestSetPassword_CreateStoresTheHashNotThePassword(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("", credentialAbsent())

	passwordHasher.EXPECT().
		Hash(ctx, "new-password-123").
		Return("$argon2id$stored-hash", nil)

	var stored string

	repository.EXPECT().
		CreatePasswordCredential(ctx, testUserID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, passwordHash string) error {
			stored = passwordHash

			return nil
		})

	_, err := service.SetPassword(ctx, setCreateParams())
	require.NoError(t, err)
	require.Equal(t, "$argon2id$stored-hash", stored)
	require.NotContains(t, stored, "new-password-123")
}

// A supplied current_password for an account with no credential is refused
// rather than ignored. Ignoring it would report success for a secret that was
// never checked.
func TestSetPassword_CreateRejectsASuppliedCurrentPassword(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("", credentialAbsent())

	params := setPasswordParams()
	params.CurrentPassword = "something-i-made-up"

	result, err := service.SetPassword(ctx, params)
	require.Equal(t, CodeUnexpectedCurrentPassword, apperror.FromError(err).Code)
	require.Equal(t, 400, apperror.FromError(err).Status)
	require.Equal(t, SetPasswordResult(""), result)

	// Neither Hash nor the insert is expected, so reaching either fails the test.
	_ = passwordHasher
}

// A concurrent creator losing on the primary key is reported as a conflict. This
// asserts the service passes the repository's answer through rather than
// reinterpreting a unique violation as a fault.
func TestSetPassword_CreateReportsAConcurrentCreatorRefusing(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("", credentialAbsent())

	passwordHasher.EXPECT().
		Hash(ctx, "new-password-123").
		Return("loser-hash", nil)

	repository.EXPECT().
		CreatePasswordCredential(ctx, testUserID, "loser-hash").
		Return(apperror.ConflictWith(
			CodePasswordCredentialAlreadyExists,
			"a password has already been set for this account",
			nil,
		))

	_, err := service.SetPassword(ctx, setCreateParams())
	require.Equal(t, CodePasswordCredentialAlreadyExists, apperror.FromError(err).Code)
	require.Equal(t, 409, apperror.FromError(err).Status)
}

// Creating a first password revokes nothing. Nothing was compromised: there was
// no credential to compromise.
func TestSetPassword_CreateRevokesNoSessions(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("", credentialAbsent())

	passwordHasher.EXPECT().
		Hash(ctx, "new-password-123").
		Return("new-hash", nil)

	// CreatePasswordCredential is the only write expected. A revocation would have
	// to go through SetPasswordAndRevokeOtherSessions, which is never expected, so
	// calling it fails this test.
	repository.EXPECT().
		CreatePasswordCredential(ctx, testUserID, "new-hash").
		Return(nil)

	result, err := service.SetPassword(ctx, setCreateParams())
	require.NoError(t, err)
	require.Equal(t, SetPasswordCreated, result)
}

// A database fault must not be read as "this account has no credential".
// Treating an outage as an absent credential would create one.
func TestSetPassword_CreateDoesNotBranchOnAFault(t *testing.T) {
	service, repository, _ := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("", apperror.Internal(errors.New("connection reset")))

	// CreatePasswordCredential is never expected: reaching the create branch on a
	// fault would fail here.
	result, err := service.SetPassword(ctx, setPasswordParams())
	require.Equal(t, apperror.CodeInternal, apperror.FromError(err).Code)
	require.Equal(t, SetPasswordResult(""), result)
}

// --- change branch ---------------------------------------------------------

func TestSetPassword_ChangeVerifiesThenHashesThenWrites(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("stored-hash", nil)

	passwordHasher.EXPECT().
		Verify(ctx, "current-password", "stored-hash").
		Return(security.PasswordVerificationResult{Match: true}, nil)

	passwordHasher.EXPECT().
		Hash(ctx, "new-password-123").
		Return("replacement-hash", nil)

	// previousPasswordHash goes into the write so a concurrent change is caught.
	repository.EXPECT().
		SetPasswordAndRevokeOtherSessions(
			ctx,
			testUserID,
			testSessionID,
			"stored-hash",
			"replacement-hash",
		).
		Return(nil)

	result, err := service.SetPassword(ctx, setPasswordParams())
	require.NoError(t, err)
	require.Equal(t, SetPasswordChanged, result)
}

// A wrong current password costs one derivation and never reaches the hasher.
// gomock fails on an unexpected call, so an extra Hash would fail this test
// rather than pass on a timing coincidence.
func TestSetPassword_ChangeNeverHashesWhenTheCurrentPasswordIsWrong(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("stored-hash", nil)

	passwordHasher.EXPECT().
		Verify(ctx, "current-password", "stored-hash").
		Return(security.PasswordVerificationResult{Match: false}, nil)

	// Hash is never expected and no write is expected.
	result, err := service.SetPassword(ctx, setPasswordParams())
	require.Equal(t, CodeCurrentPasswordInvalid, apperror.FromError(err).Code)
	require.Equal(t, 401, apperror.FromError(err).Status)
	require.Equal(t, SetPasswordResult(""), result)
}

func TestSetPassword_ChangeReportsAWrongCurrentPasswordWithoutLeakingDetail(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("stored-hash", nil)

	passwordHasher.EXPECT().
		Verify(ctx, "current-password", "stored-hash").
		Return(security.PasswordVerificationResult{Match: false}, nil)

	_, err := service.SetPassword(ctx, setPasswordParams())

	appErr := apperror.FromError(err)
	require.Equal(t, CodeCurrentPasswordInvalid, appErr.Code)
	require.NotContains(t, appErr.Message, "stored-hash")
	require.NotContains(t, appErr.Message, "argon")
}

func TestSetPassword_ChangeRequiresTheCurrentPasswordWhenOneExists(t *testing.T) {
	service, repository, _ := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("stored-hash", nil)

	params := setPasswordParams()
	params.CurrentPassword = ""

	result, err := service.SetPassword(ctx, params)
	require.Equal(t, CodeCurrentPasswordRequired, apperror.FromError(err).Code)
	require.Equal(t, 400, apperror.FromError(err).Status)
	require.Equal(t, SetPasswordResult(""), result)
}

// A lost update is an honest conflict, not a silent overwrite.
func TestSetPassword_ChangeReportsALostUpdate(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("stored-hash", nil)

	passwordHasher.EXPECT().
		Verify(ctx, "current-password", "stored-hash").
		Return(security.PasswordVerificationResult{Match: true}, nil)

	passwordHasher.EXPECT().
		Hash(ctx, "new-password-123").
		Return("replacement-hash", nil)

	repository.EXPECT().
		SetPasswordAndRevokeOtherSessions(
			ctx, testUserID, testSessionID, "stored-hash", "replacement-hash",
		).
		Return(apperror.ConflictWith(
			CodePasswordAlreadyChanged,
			"the password was changed by another request; try again",
			nil,
		))

	_, err := service.SetPassword(ctx, setPasswordParams())
	require.Equal(t, CodePasswordAlreadyChanged, apperror.FromError(err).Code)
	require.Equal(t, 409, apperror.FromError(err).Status)
}

// The caller's own session id is what the revocation spares, and it is the only
// thing it is used for.
func TestSetPassword_ChangeSparesTheCallersOwnSession(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	params := setPasswordParams()
	params.CurrentSessionID = uuid.MustParse("01a0f359-093b-737a-963a-80f7ca6768aa")

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("stored-hash", nil)

	passwordHasher.EXPECT().
		Verify(ctx, "current-password", "stored-hash").
		Return(security.PasswordVerificationResult{Match: true}, nil)

	passwordHasher.EXPECT().
		Hash(ctx, "new-password-123").
		Return("replacement-hash", nil)

	repository.EXPECT().
		SetPasswordAndRevokeOtherSessions(
			ctx, testUserID, params.CurrentSessionID, "stored-hash", "replacement-hash",
		).
		Return(nil)

	result, err := service.SetPassword(ctx, params)
	require.NoError(t, err)
	require.Equal(t, SetPasswordChanged, result)
}

// --- password-work capacity ------------------------------------------------

// A refusal to do password work at all is not invalid credentials. The caller is
// told the server is busy and given the wait, and nothing at all is written: no
// credential, no session revocation.
//
// The stub hasher makes the slot look unavailable without any real derivation, so
// no assertion here depends on how long Argon2id takes.

func TestSetPassword_CapacityExhaustionOnVerifyIs503AndWritesNothing(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("stored-hash", nil)

	passwordHasher.EXPECT().
		Verify(ctx, "current-password", "stored-hash").
		Return(security.PasswordVerificationResult{}, security.ErrPasswordWorkCapacityTimeout)

	passwordHasher.EXPECT().
		Wait().
		Return(1500 * time.Millisecond)

	// Hash and every write are never expected: gomock fails on an unexpected call,
	// so reaching either fails this test.
	appErr := apperror.FromError(mustFail(service.SetPassword(ctx, setPasswordParams())))

	require.Equal(t, 503, appErr.Status)
	require.Equal(t, CodePasswordWorkUnavailable, appErr.Code)
	require.Equal(t, "the server is busy, please try again shortly", appErr.Message)

	retryAfter := appErr.RetryAfterSeconds()
	require.NotNil(t, retryAfter)
	require.Positive(t, *retryAfter)
	require.LessOrEqual(t, *retryAfter, 2)
}

// The same condition on the hashing half of a change, after the current password
// was already verified. It must reach the caller the same way.
func TestSetPassword_CapacityExhaustionOnHashIs503AndWritesNothing(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("stored-hash", nil)

	passwordHasher.EXPECT().
		Verify(ctx, "current-password", "stored-hash").
		Return(security.PasswordVerificationResult{Match: true}, nil)

	passwordHasher.EXPECT().
		Hash(ctx, "new-password-123").
		Return("", security.ErrPasswordWorkCapacityTimeout)

	passwordHasher.EXPECT().
		Wait().
		Return(1500 * time.Millisecond)

	// No write is expected, so the credential and every session are untouched.
	appErr := apperror.FromError(mustFail(service.SetPassword(ctx, setPasswordParams())))

	require.Equal(t, 503, appErr.Status)
	require.Equal(t, CodePasswordWorkUnavailable, appErr.Code)
	require.NotNil(t, appErr.RetryAfterSeconds())
}

// A refusal on the create branch behaves identically, because the condition is
// the same regardless of which branch reached for the derivation.
func TestSetPassword_CapacityExhaustionOnCreateIs503(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("", credentialAbsent())

	passwordHasher.EXPECT().
		Hash(ctx, "new-password-123").
		Return("", security.ErrPasswordWorkCapacityTimeout)

	passwordHasher.EXPECT().
		Wait().
		Return(1500 * time.Millisecond)

	appErr := apperror.FromError(mustFail(service.SetPassword(ctx, setCreateParams())))
	require.Equal(t, 503, appErr.Status)
	require.Equal(t, CodePasswordWorkUnavailable, appErr.Code)
}

// Capacity exhaustion is not a rate limit, an input error, an authorization
// failure or a fault. It is its own answer and nothing else.
func TestSetPassword_CapacityExhaustionIsNothingElse(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("stored-hash", nil)

	passwordHasher.EXPECT().
		Verify(ctx, "current-password", "stored-hash").
		Return(security.PasswordVerificationResult{}, security.ErrPasswordWorkCapacityTimeout)

	passwordHasher.EXPECT().
		Wait().
		Return(time.Second)

	appErr := apperror.FromError(mustFail(service.SetPassword(ctx, setPasswordParams())))

	require.NotEqual(t, 429, appErr.Status)
	require.NotEqual(t, 400, appErr.Status)
	require.NotEqual(t, 401, appErr.Status)
	require.NotEqual(t, 500, appErr.Status)
	require.NotEqual(t, CodeCurrentPasswordInvalid, appErr.Code)
}

// A cancelled context is not a busy server. Folding the two together would tell
// a caller that went away to come back shortly for a request nobody is waiting
// for, so it stays a 500 and carries no Retry-After.
//
// Wait is still read, because the mapping takes the wait as an argument and
// Go evaluates arguments before the call — the same shape login, registration and
// password reset all use. Wait is a pure getter with no side effect, so reading
// it costs nothing; what matters is that its value is not emitted on a path that
// did not need it.
func TestSetPassword_CancelledContextIsNotCapacityExhaustion(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("stored-hash", nil)

	passwordHasher.EXPECT().
		Verify(ctx, "current-password", "stored-hash").
		Return(security.PasswordVerificationResult{}, context.Canceled)

	passwordHasher.EXPECT().
		Wait().
		Return(time.Second)

	appErr := apperror.FromError(mustFail(service.SetPassword(ctx, setPasswordParams())))
	require.Equal(t, 500, appErr.Status)
	require.Equal(t, apperror.CodeInternal, appErr.Code)
	require.Nil(t, appErr.RetryAfterSeconds())
}

// The identical request succeeds once capacity returns. This is what shows the
// refusal was about capacity rather than about the request.
func TestSetPassword_SucceedsOnceCapacityReturns(t *testing.T) {
	service, repository, passwordHasher := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("stored-hash", nil)

	passwordHasher.EXPECT().
		Verify(ctx, "current-password", "stored-hash").
		Return(security.PasswordVerificationResult{}, security.ErrPasswordWorkCapacityTimeout)

	passwordHasher.EXPECT().
		Wait().
		Return(time.Second)

	_, err := service.SetPassword(ctx, setPasswordParams())
	require.Error(t, err)

	// The expectation for the successful attempt is registered only now. Were it
	// registered up front, the refused attempt would have been permitted to take it.
	repository.EXPECT().
		GetPasswordCredentialForUser(ctx, testUserID).
		Return("stored-hash", nil)

	passwordHasher.EXPECT().
		Verify(ctx, "current-password", "stored-hash").
		Return(security.PasswordVerificationResult{Match: true}, nil)

	passwordHasher.EXPECT().
		Hash(ctx, "new-password-123").
		Return("replacement-hash", nil)

	repository.EXPECT().
		SetPasswordAndRevokeOtherSessions(
			ctx, testUserID, testSessionID, "stored-hash", "replacement-hash",
		).
		Return(nil)

	result, err := service.SetPassword(ctx, setPasswordParams())
	require.NoError(t, err)
	require.Equal(t, SetPasswordChanged, result)
}

// The public message names no mechanism: not Argon2, not the limiter, not the
// deployment's capacity.
func TestSetPassword_CapacityExhaustionSaysNothingInternal(t *testing.T) {
	appErr := apperror.FromError(passwordWorkError(
		security.ErrPasswordWorkCapacityTimeout,
		time.Second,
	))

	require.Equal(t, "the server is busy, please try again shortly", appErr.Message)
	require.NotContains(t, appErr.Message, "argon")
	require.NotContains(t, appErr.Message, "limiter")
	require.NotContains(t, appErr.Message, "capacity")
}

func mustFail(_ SetPasswordResult, err error) error {
	return err
}

// The capacity mapping is asserted directly rather than only through a service
// path, so the exact status, code, message and Retry-After are pinned in one
// place. passwordWorkError is the single point every refusal funnels through.
func TestPasswordWorkError_CapacityTimeoutIs503(t *testing.T) {
	err := passwordWorkError(
		security.ErrPasswordWorkCapacityTimeout,
		1500*time.Millisecond,
	)

	appErr := apperror.FromError(err)
	require.Equal(t, 503, appErr.Status)
	require.Equal(t, CodePasswordWorkUnavailable, appErr.Code)
	require.Equal(t, "the server is busy, please try again shortly", appErr.Message)

	retryAfter := appErr.RetryAfterSeconds()
	require.NotNil(t, retryAfter)
	require.Positive(t, *retryAfter)
	require.LessOrEqual(t, *retryAfter, 2)
}

// A cancelled context asks the caller to stop rather than to come back, and a
// hash this build cannot parse is a fault with nothing to act on. Neither may be
// reported as a busy server.
func TestPasswordWorkError_CancellationAndParseFaultsAreNotCapacityExhaustion(t *testing.T) {
	for _, underlying := range []error{
		context.Canceled,
		errors.New("invalid password hash format"),
	} {
		appErr := apperror.FromError(passwordWorkError(underlying, time.Second))

		require.Equal(t, 500, appErr.Status)
		require.Equal(t, apperror.CodeInternal, appErr.Code)
		require.Nil(t, appErr.RetryAfterSeconds())
	}
}
