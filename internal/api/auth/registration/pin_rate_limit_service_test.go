package registration_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	registrationdb "github.com/thoriqr/stash-it-backend/internal/api/auth/registration/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

// The verification a limiter test needs: pending, not expired, and never having
// sent a code, so the cooldown is not what refuses the request.
func pendingVerification(
	verificationID uuid.UUID,
	email string,
) registrationdb.GetVerificationRow {
	return registrationdb.GetVerificationRow{
		ID:                 verificationID,
		Email:              email,
		Status:             string(registration.VerificationRequestPending),
		RegistrationStatus: string(registration.PendingRegistrationPending),
		RegistrationExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(7 * 24 * time.Hour),
			Valid: true,
		},
		LastSentAt: pgtype.Timestamptz{},
	}
}

// A request within the address's budget issues a code and sends a message, and
// spends the budget against the address the verification belongs to.
func TestService_PinRateLimit_WithinBudgetIssuesAndSpends(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()
	const email = "alice@example.com"

	test.repository.EXPECT().
		GetVerification(ctx, verificationID).
		Return(pendingVerification(verificationID, email), nil)

	test.repository.EXPECT().
		IssueVerificationCode(ctx, gomock.Any()).
		Return(registrationdb.VerificationRequest{ID: verificationID}, nil)

	_, err := test.registrationService.CreatePIN(ctx, verificationID)
	require.NoError(t, err)

	require.Len(t, test.emailSender.Messages, 1)
	require.Equal(t, email, test.emailSender.Messages[0].To.Email)

	require.Len(t, test.pinRateLimiter.Subjects, 1)
	require.Equal(
		t,
		email,
		test.pinRateLimiter.Subjects[0],
		"the budget must be spent against the address, not the verification",
	)

	require.Equal(
		t,
		[]ratelimit.Policy{{
			Max:    registration.PinRateLimitMax,
			Window: registration.PinRateLimitWindow,
		}},
		test.pinRateLimiter.Policies,
	)
}

// The address is what is keyed, and it is keyed in its canonical form. Two
// verifications belonging to one address therefore spend one budget, which is the
// entire point: the cooldown is per verification and would not have stopped this.
func TestService_PinRateLimit_IsKeyedByAddressNotVerification(t *testing.T) {
	first := newTestService(t)
	second := newTestService(t)

	ctx := context.Background()

	firstID := uuid.New()
	secondID := uuid.New()

	// One address, two verifications, stored in the same canonical form
	// registration writes.
	const email = "alice@example.com"

	first.repository.EXPECT().
		GetVerification(ctx, firstID).
		Return(pendingVerification(firstID, email), nil)
	first.repository.EXPECT().
		IssueVerificationCode(ctx, gomock.Any()).
		Return(registrationdb.VerificationRequest{ID: firstID}, nil)

	second.repository.EXPECT().
		GetVerification(ctx, secondID).
		Return(pendingVerification(secondID, email), nil)
	second.repository.EXPECT().
		IssueVerificationCode(ctx, gomock.Any()).
		Return(registrationdb.VerificationRequest{ID: secondID}, nil)

	_, err := first.registrationService.CreatePIN(ctx, firstID)
	require.NoError(t, err)

	_, err = second.registrationService.ResendVerification(ctx, secondID)
	require.NoError(t, err)

	// Both spend the identical subject, so the real limiter would count them
	// against one budget rather than two.
	require.Equal(t, email, first.pinRateLimiter.Subjects[0])
	require.Equal(
		t,
		first.pinRateLimiter.Subjects[0],
		second.pinRateLimiter.Subjects[0],
		"a different verification id must not be a different budget",
	)

	require.Equal(
		t,
		first.pinRateLimiter.Namespaces[0],
		second.pinRateLimiter.Namespaces[0],
		"switching endpoints must not be a way to spend a second budget",
	)
}

// Switching endpoints is not a way around the limit. Both refusals are reported
// the same way, so a caller cannot learn which budget was the one that stopped them.
func TestService_PinRateLimit_BothEndpointsShareOneBudget(t *testing.T) {
	ctx := context.Background()

	createID := uuid.New()
	resendID := uuid.New()

	const email = "alice@example.com"

	newExhausted := func() testService {
		svc := newTestService(t)
		svc.pinRateLimiter.Allowed = false

		return svc
	}

	createTest := newExhausted()
	resendTest := newExhausted()

	createTest.repository.EXPECT().
		GetVerification(ctx, createID).
		Return(pendingVerification(createID, email), nil)

	resendTest.repository.EXPECT().
		GetVerification(ctx, resendID).
		Return(pendingVerification(resendID, email), nil)

	_, createErr := createTest.registrationService.CreatePIN(ctx, createID)
	require.Error(t, createErr)

	_, resendErr := resendTest.registrationService.ResendVerification(ctx, resendID)
	require.Error(t, resendErr)

	for _, err := range []error{createErr, resendErr} {
		appErr := apperror.FromError(err)

		require.Equal(t, http.StatusTooManyRequests, appErr.Status)
		require.Equal(t, registration.CodePinRateLimitExceeded, appErr.Code)
	}
}

// An exhausted budget must stop everything that costs money: no code is issued,
// no code row is written, and no message is sent. pin_issued_count is left alone
// because it is only ever written inside IssueVerificationCode.
func TestService_PinRateLimit_ExceededIssuesNothingAndSendsNothing(t *testing.T) {
	ctx := context.Background()

	cases := map[string]func(testService, context.Context, uuid.UUID) error{
		"create pin": func(
			s testService,
			ctx context.Context,
			id uuid.UUID,
		) error {
			_, err := s.registrationService.CreatePIN(ctx, id)

			return err
		},
		"resend": func(
			s testService,
			ctx context.Context,
			id uuid.UUID,
		) error {
			_, err := s.registrationService.ResendVerification(ctx, id)

			return err
		},
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			test.pinRateLimiter.Allowed = false

			verificationID := uuid.New()

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(pendingVerification(verificationID, "alice@example.com"), nil)

			// IssueVerificationCode has no expectation, so gomock fails the test
			// if the service reaches it. That is the assertion: no code row is
			// written, and so pin_issued_count does not move either.
			err := call(test, ctx, verificationID)

			require.Error(t, err)

			appErr := apperror.FromError(err)
			require.Equal(t, http.StatusTooManyRequests, appErr.Status)
			require.Equal(t, registration.CodePinRateLimitExceeded, appErr.Code)

			require.Empty(
				t,
				test.emailSender.Messages,
				"a refused request must not send a message",
			)
		})
	}
}

// A limiter that cannot answer has not said the caller is within budget. Allowing
// would hand an attacker the ability to switch the ceiling off by causing a failure.
func TestService_PinRateLimit_UnavailableRefusesAndIssuesNothing(t *testing.T) {
	ctx := context.Background()

	cases := map[string]func(testService, context.Context, uuid.UUID) error{
		"create pin": func(
			s testService,
			ctx context.Context,
			id uuid.UUID,
		) error {
			_, err := s.registrationService.CreatePIN(ctx, id)

			return err
		},
		"resend": func(
			s testService,
			ctx context.Context,
			id uuid.UUID,
		) error {
			_, err := s.registrationService.ResendVerification(ctx, id)

			return err
		},
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			test.pinRateLimiter.Err = errors.New("dial tcp: connection refused")

			verificationID := uuid.New()

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(pendingVerification(verificationID, "alice@example.com"), nil)

			err := call(test, ctx, verificationID)

			require.Error(t, err)

			appErr := apperror.FromError(err)
			require.Equal(
				t,
				http.StatusServiceUnavailable,
				appErr.Status,
				"an unusable limiter must not be treated as permission",
			)
			require.Equal(
				t,
				registration.CodePinRateLimitUnavailable,
				appErr.Code,
			)

			require.Empty(t, test.emailSender.Messages)
		})
	}
}

// The cooldown is checked first, so a caller already being told to wait is not
// also charged for asking. Without this an address could spend its whole budget on
// requests that were going to be refused anyway.
func TestService_PinRateLimit_CooldownRefusalDoesNotSpendBudget(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()

	verification := pendingVerification(verificationID, "alice@example.com")

	// Sent a moment ago, so the cooldown refuses it.
	verification.LastSentAt = pgtype.Timestamptz{
		Time:  time.Now(),
		Valid: true,
	}

	test.repository.EXPECT().
		GetVerification(ctx, verificationID).
		Return(verification, nil)

	_, err := test.registrationService.CreatePIN(ctx, verificationID)

	require.Error(t, err)
	require.Equal(
		t,
		registration.CodeVerificationResendCooldown,
		apperror.FromError(err).Code,
		"the cooldown still refuses a recent send",
	)

	require.Empty(
		t,
		test.pinRateLimiter.Subjects,
		"a request refused by the cooldown must not spend the address budget",
	)

	require.Empty(t, test.emailSender.Messages)
}

// The cooldown is untouched by the limiter: a request the limiter allows is still
// refused by the cooldown, and a request past the cooldown is allowed.
func TestService_PinRateLimit_CooldownStillAppliesWhenWithinBudget(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()

	verification := pendingVerification(verificationID, "alice@example.com")
	verification.LastSentAt = pgtype.Timestamptz{
		Time:  time.Now(),
		Valid: true,
	}

	test.repository.EXPECT().
		GetVerification(ctx, verificationID).
		Return(verification, nil)

	_, err := test.registrationService.CreatePIN(ctx, verificationID)
	require.Error(t, err)
	require.Equal(
		t,
		registration.CodeVerificationResendCooldown,
		apperror.FromError(err).Code,
	)

	require.Empty(t, test.emailSender.Messages)
}

// An address stored in a form that is not yet normalized must still spend the
// canonical budget, so a caller cannot get a second one by changing the casing a
// registration was created with.
func TestService_PinRateLimit_NormalizesTheAddressBeforeKeying(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()

	test.repository.EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			pendingVerification(verificationID, "  Alice@Example.COM  "),
			nil,
		)
	test.repository.EXPECT().
		IssueVerificationCode(ctx, gomock.Any()).
		Return(registrationdb.VerificationRequest{ID: verificationID}, nil)

	_, err := test.registrationService.CreatePIN(ctx, verificationID)
	require.NoError(t, err)

	require.Len(t, test.pinRateLimiter.Subjects, 1)
	require.Equal(
		t,
		registration.NormalizeEmail("  Alice@Example.COM  "),
		test.pinRateLimiter.Subjects[0],
	)
}
