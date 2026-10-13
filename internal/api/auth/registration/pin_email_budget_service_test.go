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
	"github.com/thoriqr/stash-it-backend/internal/email"
)

// The verification a budget test needs: pending, not expired, and never having
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

// issueCall is one of the two routes that spend the budget. Both are driven
// through the same table in every ordering and refusal test below, because they
// run one workflow and the order inside it is the property under test: an edit
// that moved a reservation would change one route and not the other, and a test
// that only drove one of them would not notice.
func issueCalls() map[string]func(
	testService, context.Context, uuid.UUID,
) error {
	return map[string]func(testService, context.Context, uuid.UUID) error{
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
}

// A request within both budgets issues a code, sends a message, and spends the
// per-address budget against the address the verification belongs to rather than
// against the verification.
func TestService_PINEmailBudget_WithinBudgetIssuesAndSpends(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()
	const address = "alice@example.com"

	test.repository.EXPECT().
		GetVerification(ctx, verificationID).
		Return(pendingVerification(verificationID, address), nil)

	test.repository.EXPECT().
		IssueVerificationCode(ctx, gomock.Any()).
		Return(registrationdb.VerificationRequest{ID: verificationID}, nil)

	_, err := test.registrationService.CreatePIN(ctx, verificationID)
	require.NoError(t, err)

	require.Len(t, test.emailSender.Messages, 1)
	require.Equal(t, address, test.emailSender.Messages[0].To.Email)

	require.Equal(
		t,
		[]string{address},
		test.pinEmailBudget.PerEmailSubjects,
		"the budget must be spent against the address, not the verification",
	)

	require.Equal(
		t,
		1,
		test.pinEmailBudget.GlobalCalls,
		"every PIN spends the deployment-wide budget",
	)
}

// The address is what is keyed, and it is keyed in its canonical form. Two
// verifications belonging to one address therefore spend one budget, which is the
// entire point: the cooldown is per verification and would not have stopped this.
func TestService_PINEmailBudget_IsKeyedByAddressNotVerification(t *testing.T) {
	first := newTestService(t)
	second := newTestService(t)

	ctx := context.Background()

	firstID := uuid.New()
	secondID := uuid.New()

	// One address, two verifications, stored in the same canonical form
	// registration writes.
	const address = "alice@example.com"

	first.repository.EXPECT().
		GetVerification(ctx, firstID).
		Return(pendingVerification(firstID, address), nil)
	first.repository.EXPECT().
		IssueVerificationCode(ctx, gomock.Any()).
		Return(registrationdb.VerificationRequest{ID: firstID}, nil)

	second.repository.EXPECT().
		GetVerification(ctx, secondID).
		Return(pendingVerification(secondID, address), nil)
	second.repository.EXPECT().
		IssueVerificationCode(ctx, gomock.Any()).
		Return(registrationdb.VerificationRequest{ID: secondID}, nil)

	_, err := first.registrationService.CreatePIN(ctx, firstID)
	require.NoError(t, err)

	_, err = second.registrationService.ResendVerification(ctx, secondID)
	require.NoError(t, err)

	require.NotEmpty(t, first.pinEmailBudget.PerEmailSubjects)
	require.NotEmpty(t, second.pinEmailBudget.PerEmailSubjects)

	require.Equal(
		t,
		first.pinEmailBudget.PerEmailSubjects[0],
		second.pinEmailBudget.PerEmailSubjects[0],
		"a different verification id must not be a different budget",
	)
}

// Switching endpoints is not a way around the limit, and both refusals are
// reported the same way, so a caller cannot learn which budget stopped them.
func TestService_PINEmailBudget_BothEndpointsShareOneBudget(t *testing.T) {
	ctx := context.Background()

	createID := uuid.New()
	resendID := uuid.New()

	const address = "alice@example.com"

	newExhausted := func() testService {
		svc := newTestService(t)
		svc.pinEmailBudget.PerEmailAllowed = false

		return svc
	}

	createTest := newExhausted()
	resendTest := newExhausted()

	createTest.repository.EXPECT().
		GetVerification(ctx, createID).
		Return(pendingVerification(createID, address), nil)

	resendTest.repository.EXPECT().
		GetVerification(ctx, resendID).
		Return(pendingVerification(resendID, address), nil)

	_, createErr := createTest.registrationService.CreatePIN(ctx, createID)
	require.Error(t, createErr)

	_, resendErr := resendTest.registrationService.ResendVerification(
		ctx,
		resendID,
	)
	require.Error(t, resendErr)

	for _, err := range []error{createErr, resendErr} {
		appErr := apperror.FromError(err)

		require.Equal(t, http.StatusTooManyRequests, appErr.Status)
		require.Equal(t, registration.CodePinRateLimitExceeded, appErr.Code)
	}
}

// An exhausted per-address budget must stop everything that costs money: no code
// is issued, no row is written, and no message is sent. pin_issued_count is left
// alone because it is only ever written inside IssueVerificationCode, so gomock
// failing on that call is the assertion that neither happened.
func TestService_PINEmailBudget_PerEmailExceededIssuesNothingAndSendsNothing(
	t *testing.T,
) {
	ctx := context.Background()

	for name, call := range issueCalls() {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			test.pinEmailBudget.PerEmailAllowed = false

			verificationID := uuid.New()

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(pendingVerification(verificationID, "alice@example.com"), nil)

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

			require.Equal(
				t,
				0,
				test.pinEmailBudget.GlobalCalls,
				"a per-address refusal must not spend the deployment budget",
			)
		})
	}
}

// A deployment that cannot send anything refuses every address, and it is a
// different answer from the one an exhausted address gets: a client can fix the
// second by waiting and can do nothing at all about the first.
func TestService_PINEmailBudget_GlobalExceededIssuesNothingAndSendsNothing(
	t *testing.T,
) {
	ctx := context.Background()

	for name, call := range issueCalls() {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			test.pinEmailBudget.GlobalAllowed = false

			verificationID := uuid.New()

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(pendingVerification(verificationID, "alice@example.com"), nil)

			err := call(test, ctx, verificationID)

			require.Error(t, err)

			appErr := apperror.FromError(err)
			require.Equal(t, http.StatusTooManyRequests, appErr.Status)
			require.Equal(t, registration.CodePINEmailBudgetExceeded, appErr.Code)

			require.Empty(t, test.emailSender.Messages)

			require.Zero(
				t,
				test.pinEmailBudget.RefundCount(),
				"a refused reservation was never an admission to give back",
			)
		})
	}
}

// The per-address budget is checked before the deployment-wide one. A caller
// refused for its own address must not also spend a slot of the whole
// deployment's budget, because that slot is worth far more than its own.
func TestService_PINEmailBudget_PerEmailRefusalDoesNotSpendGlobalBudget(
	t *testing.T,
) {
	ctx := context.Background()

	for name, call := range issueCalls() {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			test.pinEmailBudget.PerEmailAllowed = false

			verificationID := uuid.New()

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(pendingVerification(verificationID, "alice@example.com"), nil)

			err := call(test, ctx, verificationID)
			require.Error(t, err)

			require.Equal(t, 1, test.pinEmailBudget.PerEmailCalls)
			require.Equal(
				t,
				0,
				test.pinEmailBudget.GlobalCalls,
				"the deployment budget must only be spent by a request that "+
					"passed its own address's budget",
			)
		})
	}
}

// A budget that cannot be evaluated has not said the caller is within budget.
// Allowing on failure would hand an attacker the ability to switch the ceiling
// off by causing the failure that produces it.
func TestService_PINEmailBudget_UnavailableRefusesAndIssuesNothing(t *testing.T) {
	ctx := context.Background()

	cases := map[string]struct {
		prepare func(*FakePINEmailBudget)
		code    string
	}{
		"per address": {
			prepare: func(b *FakePINEmailBudget) {
				b.PerEmailErr = errors.New("dial tcp: connection refused")
			},
			code: registration.CodePinRateLimitUnavailable,
		},
		"global": {
			prepare: func(b *FakePINEmailBudget) {
				b.GlobalErr = errors.New("dial tcp: connection refused")
			},
			code: registration.CodePINEmailBudgetUnavailable,
		},
	}

	for budgetName, budgetCase := range cases {
		for name, call := range issueCalls() {
			t.Run(budgetName+" "+name, func(t *testing.T) {
				test := newTestService(t)
				budgetCase.prepare(test.pinEmailBudget)

				verificationID := uuid.New()

				test.repository.EXPECT().
					GetVerification(ctx, verificationID).
					Return(
						pendingVerification(verificationID, "alice@example.com"),
						nil,
					)

				err := call(test, ctx, verificationID)
				require.Error(t, err)

				appErr := apperror.FromError(err)

				require.Equal(
					t,
					http.StatusServiceUnavailable,
					appErr.Status,
					"an unusable budget must not be treated as permission",
				)
				require.Equal(t, budgetCase.code, appErr.Code)

				require.Empty(t, test.emailSender.Messages)
			})
		}
	}
}

// The cooldown is checked before either budget, so a caller already being told to
// wait is not also charged for asking. Without this an address could spend its
// whole budget on requests that were going to be refused anyway — and with a ten
// a day allowance that is one the user cannot get back until tomorrow.
func TestService_PINEmailBudget_CooldownRefusalDoesNotSpendEitherBudget(
	t *testing.T,
) {
	ctx := context.Background()

	for name, call := range issueCalls() {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			verificationID := uuid.New()

			verification := pendingVerification(
				verificationID,
				"alice@example.com",
			)

			// Sent a moment ago, so the cooldown refuses it.
			verification.LastSentAt = pgtype.Timestamptz{
				Time:  time.Now(),
				Valid: true,
			}

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(verification, nil)

			err := call(test, ctx, verificationID)

			require.Error(t, err)
			require.Equal(
				t,
				registration.CodeVerificationResendCooldown,
				apperror.FromError(err).Code,
				"the cooldown still refuses a recent send",
			)

			require.Equal(
				t,
				0,
				test.pinEmailBudget.PerEmailCalls,
				"a request refused by the cooldown must not spend the address budget",
			)
			require.Equal(
				t,
				0,
				test.pinEmailBudget.GlobalCalls,
				"a request refused by the cooldown must not spend the "+
					"deployment budget",
			)

			require.Empty(t, test.emailSender.Messages)
		})
	}
}

// The cooldown is untouched by the budgets: a request both budgets admit is
// still refused by the cooldown.
func TestService_PINEmailBudget_CooldownStillAppliesWhenWithinBudget(t *testing.T) {
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
func TestService_PINEmailBudget_NormalizesTheAddressBeforeKeying(t *testing.T) {
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

	require.Equal(
		t,
		[]string{registration.NormalizeEmail("  Alice@Example.COM  ")},
		test.pinEmailBudget.PerEmailSubjects,
	)
}

// A reservation whose issuing transaction fails is given back. Nothing has been
// sent at that point, so the slot is provably unused, and keeping it would mean
// one broken row cost this deployment a day's worth of mail allowance.
func TestService_PINEmailBudget_DatabaseFailureRefundsTheGlobalReservation(
	t *testing.T,
) {
	ctx := context.Background()

	for name, call := range issueCalls() {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			verificationID := uuid.New()

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(
					pendingVerification(verificationID, "alice@example.com"),
					nil,
				)

			test.repository.EXPECT().
				IssueVerificationCode(ctx, gomock.Any()).
				Return(
					registrationdb.VerificationRequest{},
					apperror.Internal(errors.New("deadlock detected")),
				)

			err := call(test, ctx, verificationID)
			require.Error(t, err)

			require.Equal(
				t,
				1,
				test.pinEmailBudget.RefundCount(),
				"a failure strictly before the provider gives the slot back",
			)

			require.Empty(
				t,
				test.emailSender.Messages,
				"a failed issuance must not send a message",
			)
		})
	}
}

// A sender that reached a provider and then failed keeps its slot. A timeout
// does not prove the message was declined, so refunding it would hand back
// capacity a message may already have been delivered against.
func TestService_PINEmailBudget_AttemptedSendFailureKeepsTheGlobalReservation(
	t *testing.T,
) {
	ctx := context.Background()

	for name, call := range issueCalls() {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			test.emailSender.Err = errors.New("read: connection reset by peer")

			verificationID := uuid.New()

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(
					pendingVerification(verificationID, "alice@example.com"),
					nil,
				)
			test.repository.EXPECT().
				IssueVerificationCode(ctx, gomock.Any()).
				Return(
					registrationdb.VerificationRequest{ID: verificationID},
					nil,
				)

			err := call(test, ctx, verificationID)
			require.Error(t, err)

			require.Equal(
				t,
				0,
				test.pinEmailBudget.RefundCount(),
				"an attempted send keeps its slot whatever it returns",
			)

			appErr := apperror.FromError(err)
			require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
			require.Equal(t, registration.CodePINEmailDeliveryFailed, appErr.Code)
			require.NotNil(
				t,
				appErr.RetryAfterSeconds(),
				"a delivery failure must say when to come back",
			)
		})
	}
}

// A sender that never tried gives its slot back. This is the case the production
// UnconfiguredSender is in: it makes no network call, so nothing could have been
// accepted anywhere, and a staging deployment must not exhaust a real provider's
// budget by failing to send.
func TestService_PINEmailBudget_SenderThatNeverTriedRefundsTheReservation(
	t *testing.T,
) {
	ctx := context.Background()

	for name, call := range issueCalls() {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			test.emailSender.Err = email.NotAttempted(
				errors.New("email sender is not configured"),
			)

			verificationID := uuid.New()

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(
					pendingVerification(verificationID, "alice@example.com"),
					nil,
				)
			test.repository.EXPECT().
				IssueVerificationCode(ctx, gomock.Any()).
				Return(
					registrationdb.VerificationRequest{ID: verificationID},
					nil,
				)

			err := call(test, ctx, verificationID)
			require.Error(t, err)

			require.Equal(
				t,
				1,
				test.pinEmailBudget.RefundCount(),
				"a sender that never tried gives the slot back",
			)
		})
	}
}

// The Retry-After on a budget refusal is the wait the budget itself reported, not
// a configured constant. It comes from the oldest entry still inside the rolling
// window, so it is the moment a slot actually frees.
func TestService_PINEmailBudget_RetryAfterComesFromTheBudget(t *testing.T) {
	ctx := context.Background()

	cases := map[string]struct {
		prepare     func(*FakePINEmailBudget)
		wantSeconds int
	}{
		"per address": {
			prepare: func(b *FakePINEmailBudget) {
				b.PerEmailAllowed = false
				b.PerEmailRetryAfter = 37 * time.Second
			},
			wantSeconds: 37,
		},
		"global": {
			prepare: func(b *FakePINEmailBudget) {
				b.GlobalAllowed = false
				b.GlobalRetryAfter = 4 * time.Minute
			},
			wantSeconds: 240,
		},
	}

	for budgetName, budgetCase := range cases {
		t.Run(budgetName, func(t *testing.T) {
			test := newTestService(t)
			budgetCase.prepare(test.pinEmailBudget)

			verificationID := uuid.New()

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(
					pendingVerification(verificationID, "alice@example.com"),
					nil,
				)

			_, err := test.registrationService.CreatePIN(ctx, verificationID)
			require.Error(t, err)

			seconds := apperror.FromError(err).RetryAfterSeconds()
			require.NotNil(t, seconds)
			require.Equal(t, budgetCase.wantSeconds, *seconds)
		})
	}
}
