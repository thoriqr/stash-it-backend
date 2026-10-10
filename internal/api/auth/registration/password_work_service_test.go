package registration_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	registrationdb "github.com/thoriqr/stash-it-backend/internal/api/auth/registration/generated"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration/mocks"
	sessionmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/session/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/email"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// Registration hashes a password exactly as login verifies one and costs the same
// memory doing it, so it is bounded by the same limit rather than one of its own.
// These cover the refusal.
//
// The single slot is held from outside the request, so nothing here waits for a
// real derivation or depends on how fast the machine is.

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

// newTestServiceWithHasher builds the registration service over a caller-supplied
// hasher, so a test can decide how much capacity the request will find.
func newTestServiceWithHasher(
	t *testing.T,
	hasher *security.PasswordHasher,
) (testService, *mocks.MockRepository) {
	t.Helper()

	ctrl := gomock.NewController(t)

	repository := mocks.NewMockRepository(ctrl)

	service := registration.NewService(
		repository,
		sessionmocks.NewMockSessionCreator(ctrl),
		security.NewAccessTokenGenerator([]byte("test-secret")),
		hasher,
		security.NewVerificationCodeHasher([]byte("test-secret")),
		email.Sender(nil),
		NewFakePinRateLimiter(),
	)

	return testService{
		registrationService: service,
		socialService:       service,
		repository:          repository,
		emailSender:         &FakeEmailSender{},
		pinRateLimiter:      NewFakePinRateLimiter(),
	}, repository
}

// expectPendingContinuation makes the continuation lookup answer with a live
// manual registration, so a request reaches the hashing step.
func expectPendingContinuation(
	t *testing.T,
	repository *mocks.MockRepository,
	tokenHash string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()

	continuationID := uuid.New()
	pendingRegistrationID := uuid.New()

	repository.EXPECT().
		GetRegistrationContinuation(gomock.Any(), tokenHash).
		Return(
			registrationdb.GetRegistrationContinuationRow{
				ID:                    continuationID,
				PendingRegistrationID: pendingRegistrationID,
				Email:                 "alice@example.com",
				RegistrationType:      string(registration.RegistrationTypeManual),
				ExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(15 * time.Minute),
					Valid: true,
				},
				ConsumedAt: pgtype.Timestamptz{Valid: false},
				RegistrationStatus: string(
					registration.PendingRegistrationPending,
				),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(7 * 24 * time.Hour),
					Valid: true,
				},
			},
			nil,
		).
		AnyTimes()

	return continuationID, pendingRegistrationID
}

const finalizeInputToken = "a-continuation-token"

// hashedToken is what the repository is asked for: the service stores the token's
// hash, never the token itself.
func hashedToken(token string) string {
	return security.HashToken(token)
}

// Finalizing a manual registration with no capacity is refused with a 503 and a
// bounded Retry-After.
//
// It must not be a fault: nothing is broken, the server is momentarily full. It
// must not be invalid input either: nothing was wrong with the password, and
// telling a caller theirs is unacceptable would send them off to choose a
// different one.
func TestService_FinalizeManualRegistration_CapacityExhaustionIs503(t *testing.T) {
	hasher, release := saturatedHasher(t)
	defer release()

	test, repository := newTestServiceWithHasher(t, hasher)
	expectPendingContinuation(t, repository, hashedToken(finalizeInputToken))

	// No FinalizeManualRegistration expectation: the refusal must happen before
	// anything is written, and gomock failing the test is that assertion.
	_, err := test.registrationService.FinalizeManualRegistration(
		context.Background(),
		registration.FinalizeManualRegistrationInput{
			ContinuationToken: finalizeInputToken,
			DisplayName:       "Alice",
			Password:          "correct-password",
		},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)

	require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
	require.Equal(t, registration.CodePasswordWorkUnavailable, appErr.Code)

	require.NotNil(t, appErr.RetryAfterSeconds())
	require.Positive(t, *appErr.RetryAfterSeconds())
	require.LessOrEqual(t, *appErr.RetryAfterSeconds(), 1)
}

// The refusal is neither a rate limit nor an input problem.
//
// Registration has its own PIN budgets with their own codes; reusing one would
// tell the caller a budget was spent when none was, and would send a client
// looking for the wrong recovery.
func TestService_FinalizeManualRegistration_CapacityExhaustionIsNotAnythingElse(t *testing.T) {
	hasher, release := saturatedHasher(t)
	defer release()

	test, repository := newTestServiceWithHasher(t, hasher)
	expectPendingContinuation(t, repository, hashedToken(finalizeInputToken))

	_, err := test.registrationService.FinalizeManualRegistration(
		context.Background(),
		registration.FinalizeManualRegistrationInput{
			ContinuationToken: finalizeInputToken,
			DisplayName:       "Alice",
			Password:          "correct-password",
		},
	)

	appErr := apperror.FromError(err)

	require.NotEqual(t, registration.CodePinRateLimitExceeded, appErr.Code)
	require.NotEqual(t, registration.CodePinRateLimitUnavailable, appErr.Code)
	require.NotEqual(t, http.StatusTooManyRequests, appErr.Status)
	require.NotEqual(t, http.StatusBadRequest, appErr.Status)
	require.NotEqual(t, http.StatusInternalServerError, appErr.Status)
}

// The public message says the server is busy and nothing about how busy.
//
// Capacity numbers, the configured limit and the wait are all things an operator
// needs and a caller does not, and a message that varied with them would tell a
// caller which deployment they were talking to.
func TestService_FinalizeManualRegistration_CapacityExhaustionSaysNothingInternal(t *testing.T) {
	hasher, release := saturatedHasher(t)
	defer release()

	test, repository := newTestServiceWithHasher(t, hasher)
	expectPendingContinuation(t, repository, hashedToken(finalizeInputToken))

	_, err := test.registrationService.FinalizeManualRegistration(
		context.Background(),
		registration.FinalizeManualRegistrationInput{
			ContinuationToken: finalizeInputToken,
			DisplayName:       "Alice",
			Password:          "correct-password",
		},
	)

	message := apperror.FromError(err).Message

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

// Capacity that comes back makes the same registration succeed.
//
// The continuation is only valid while pending, so a refusal that consumed it
// would turn a momentarily busy server into a permanently unregistrable user.
// The absence of a second lookup expectation below is what proves it did not.
func TestService_FinalizeManualRegistration_SucceedsOnceCapacityReturns(t *testing.T) {
	hasher, release := saturatedHasher(t)

	test, repository := newTestServiceWithHasher(t, hasher)
	_, pendingRegistrationID := expectPendingContinuation(
		t,
		repository,
		hashedToken(finalizeInputToken),
	)

	_, err := test.registrationService.FinalizeManualRegistration(
		context.Background(),
		registration.FinalizeManualRegistrationInput{
			ContinuationToken: finalizeInputToken,
			DisplayName:       "Alice",
			Password:          "correct-password",
		},
	)

	require.Equal(
		t,
		registration.CodePasswordWorkUnavailable,
		apperror.FromError(err).Code,
	)

	release()

	repository.EXPECT().
		FinalizeManualRegistration(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			params registration.FinalizeManualRegistrationParams,
		) (registrationdb.CreateUserRow, error) {
			require.Equal(
				t,
				pendingRegistrationID,
				params.PendingRegistrationID,
			)

			return registrationdb.CreateUserRow{
				ID:          uuid.New(),
				Email:       "alice@example.com",
				DisplayName: "Alice",
			}, nil
		})

	result, err := test.registrationService.FinalizeManualRegistration(
		context.Background(),
		registration.FinalizeManualRegistrationInput{
			ContinuationToken: finalizeInputToken,
			DisplayName:       "Alice",
			Password:          "correct-password",
		},
	)

	require.NoError(t, err)
	require.Equal(t, "alice@example.com", result.Email)
	require.NotEmpty(t, result.UserID)
}
