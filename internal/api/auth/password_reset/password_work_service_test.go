package password_reset_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// Finalization hashes a password exactly as login verifies one and registration
// hashes one, and costs the same memory doing it, so all three are bounded by the
// same shared limit rather than one of their own. These cover what this feature
// does when that limit refuses to admit the work.
//
// The single slot is held from outside the request, so nothing here waits for a
// real derivation or depends on how fast the machine is.
//
// One absence carries most of the weight below: no
// UpsertPasswordCredentialAndCompleteReset expectation is ever registered. gomock
// fails the test on an unexpected call, so on every capacity path that absence is
// the assertion that the refusal happened before the finalization transaction was
// entered — nothing consumed, completed, written or revoked.

// saturatedHasher returns a hasher whose only slot is already taken, plus the
// release that gives it back.
func saturatedHasher(t *testing.T) (*security.PasswordHasher, func()) {
	t.Helper()

	limiter, err := security.NewPasswordWorkLimiter(1, time.Second)
	require.NoError(t, err)

	release, err := limiter.Acquire(context.Background())
	require.NoError(t, err)

	return security.NewPasswordHasher(limiter), release
}

// newTestServiceWithHasher builds the password reset service over a
// caller-supplied hasher, so a test can decide how much capacity the request will
// find.
func newTestServiceWithHasher(
	t *testing.T,
	hasher *security.PasswordHasher,
) *testService {
	t.Helper()

	ctrl := gomock.NewController(t)

	repository := mocks.NewMockRepository(ctrl)

	return &testService{
		passwordResetService: passwordreset.NewService(
			repository,
			hasher,
			security.NewVerificationCodeHasher([]byte("test-secret")),
			&FakeEmailSender{},
		),
		repository:  repository,
		emailSender: &FakeEmailSender{},
	}
}

const (
	finalizeInputToken    = "a-password-reset-continuation-token"
	finalizeInputPassword = "correct-password"
	finalizeInputEmail    = "alice@example.com"
)

// expectFinalizableContinuation makes the two reads a finalization performs before
// it hashes answer with a live continuation and the user that owns it, so the
// request reaches the hashing step.
//
// Both are AnyTimes because the recovery test runs the same request twice, and
// neither is a mutating call.
func expectFinalizableContinuation(
	t *testing.T,
	repository *mocks.MockRepository,
) {
	t.Helper()

	row := pendingContinuation(finalizeInputEmail)
	row.ID = uuid.New()
	row.PendingPasswordResetID = uuid.New()

	repository.EXPECT().
		GetPasswordResetContinuation(
			gomock.Any(),
			security.HashToken(finalizeInputToken),
		).
		Return(row, nil).
		AnyTimes()

	repository.EXPECT().
		GetUserByEmail(gomock.Any(), finalizeInputEmail).
		Return(
			passwordresetdb.GetUserByEmailRow{
				ID:    uuid.New(),
				Email: finalizeInputEmail,
			},
			nil,
		).
		AnyTimes()
}

func finalizeWith(
	ctx context.Context,
	test *testService,
) error {
	return test.passwordResetService.FinalizePasswordReset(
		ctx,
		passwordreset.FinalizePasswordResetInput{
			ContinuationToken: finalizeInputToken,
			Password:          finalizeInputPassword,
		},
	)
}

// Finalizing with no password-work capacity is refused with a 503 and a bounded
// Retry-After.
//
// It must not be a fault: nothing is broken, the server is momentarily full. It
// must not be invalid input either: nothing was wrong with the password, and
// telling a caller theirs is unacceptable would send them off to choose a
// different one.
//
// The value matches login's and registration's deliberately. It is the same
// condition with the same recovery for every caller that performs password work.
// The declarations stay in separate packages so a change to one cannot silently
// change another.
func TestService_FinalizePasswordReset_CapacityExhaustionIs503(t *testing.T) {
	hasher, release := saturatedHasher(t)
	defer release()

	test := newTestServiceWithHasher(t, hasher)
	expectFinalizableContinuation(t, test.repository)

	err := finalizeWith(context.Background(), test)
	require.Error(t, err)

	appErr := apperror.FromError(err)

	require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
	require.Equal(
		t,
		"PASSWORD_WORK_UNAVAILABLE",
		appErr.Code,
		"the value must match the login and registration features'",
	)
	require.Equal(t, passwordreset.CodePasswordWorkUnavailable, appErr.Code)

	require.NotNil(
		t,
		appErr.RetryAfterSeconds(),
		"a capacity refusal must say when to come back",
	)
	require.Positive(t, *appErr.RetryAfterSeconds())
	require.LessOrEqual(t, *appErr.RetryAfterSeconds(), 1)
}

// The refusal is neither a rate limit nor an input problem, and it is not a 500.
//
// Password reset carries its own per-client-address budget on this route, so a 429
// or a budget code here would name a limit that was never consulted and send a
// client looking for a window that does not exist.
func TestService_FinalizePasswordReset_CapacityExhaustionIsNotAnythingElse(t *testing.T) {
	hasher, release := saturatedHasher(t)
	defer release()

	test := newTestServiceWithHasher(t, hasher)
	expectFinalizableContinuation(t, test.repository)

	appErr := apperror.FromError(
		finalizeWith(context.Background(), test),
	)

	require.NotEqual(t, http.StatusTooManyRequests, appErr.Status)
	require.NotEqual(t, http.StatusBadRequest, appErr.Status)
	require.NotEqual(t, http.StatusUnauthorized, appErr.Status)
	require.NotEqual(t, http.StatusInternalServerError, appErr.Status)
}

// The public message says the server is busy and nothing about how busy.
//
// Capacity numbers, the configured limit and the wait are all things an operator
// needs and a caller does not, and a message that varied with them would tell a
// caller which deployment they were talking to.
func TestService_FinalizePasswordReset_CapacityExhaustionSaysNothingInternal(t *testing.T) {
	hasher, release := saturatedHasher(t)
	defer release()

	test := newTestServiceWithHasher(t, hasher)
	expectFinalizableContinuation(t, test.repository)

	message := apperror.FromError(
		finalizeWith(context.Background(), test),
	).Message

	require.Equal(t, "the server is busy, please try again shortly", message)

	for _, internal := range []string{
		"argon", "memory", "concurrency", "slot", "64",
		"password work capacity", "limiter",
	} {
		require.NotContains(
			t,
			message,
			internal,
			"the public message must not describe the mechanism",
		)
	}
}

// Capacity that comes back makes the same request succeed.
//
// This is what makes the Retry-After a real instruction rather than a dead end.
// The continuation is only usable while pending, so a refusal that consumed it
// would turn a momentarily busy server into a user who can never finish the
// reset.
func TestService_FinalizePasswordReset_SucceedsOnceCapacityReturns(t *testing.T) {
	hasher, release := saturatedHasher(t)

	test := newTestServiceWithHasher(t, hasher)
	expectFinalizableContinuation(t, test.repository)

	err := finalizeWith(context.Background(), test)

	require.Equal(
		t,
		passwordreset.CodePasswordWorkUnavailable,
		apperror.FromError(err).Code,
	)

	release()

	// The only mutating expectation in this file. It is registered after the
	// refusal rather than before it, so registering it up front would have
	// permitted the refusal path to take it.
	test.repository.EXPECT().
		UpsertPasswordCredentialAndCompleteReset(gomock.Any(), gomock.Any()).
		Return(nil)

	require.NoError(t, finalizeWith(context.Background(), test))
}

// A caller that goes away is not told the server is busy.
//
// This is the one error the capacity mapping must not swallow. The hasher reports
// a cancelled context and a capacity timeout through the same return value, and
// they ask the caller to do opposite things: come back shortly, or stop. Folding
// them together would report a request nobody is waiting for as merely busy.
//
// The repository reads are stubbed so they still answer under the cancelled
// context and the request actually reaches the hashing step, which is where the
// distinction is made.
func TestService_FinalizePasswordReset_CancelledContextIsNotCapacityExhaustion(t *testing.T) {
	hasher, release := saturatedHasher(t)
	defer release()

	test := newTestServiceWithHasher(t, hasher)
	expectFinalizableContinuation(t, test.repository)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := finalizeWith(ctx, test)
	require.Error(t, err)

	appErr := apperror.FromError(err)

	require.NotEqual(
		t,
		passwordreset.CodePasswordWorkUnavailable,
		appErr.Code,
		"a cancelled request must not be reported as a busy server",
	)
	require.Equal(
		t,
		http.StatusInternalServerError,
		appErr.Status,
		"a cancellation stays a fault, which is the behaviour before this change",
	)
	require.Nil(
		t,
		appErr.RetryAfterSeconds(),
		"there is nothing to come back to, so there is no wait to name",
	)
}
