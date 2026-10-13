package password_reset_test

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

	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/email"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
)

// These mirror the registration feature's budget tests deliberately. The two
// features share one budget, and a policy proven on one of them and merely
// assumed on the other is exactly how two features drift apart on a shared
// allowance — so both are driven through the same cases, on both entry points.

// budgetVerification is the verification a budget test needs: pending, not
// expired, and never having sent a code, so the cooldown is not what refuses the
// request.
func budgetVerification(
	verificationID uuid.UUID,
	emailAddress string,
) passwordresetdb.GetVerificationRow {
	return passwordresetdb.GetVerificationRow{
		ID:                  verificationID,
		Email:               emailAddress,
		Status:              string(passwordreset.VerificationRequestPending),
		PasswordResetStatus: string(passwordreset.PendingPasswordResetPending),
		PasswordResetExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(7 * 24 * time.Hour),
			Valid: true,
		},
		LastSentAt: pgtype.Timestamptz{},
	}
}

// budgetEntryPoints are the two routes that spend the budget. They are two
// spellings of the same act, and both are driven because an edit that moved a
// reservation would change one route and not the other.
func budgetEntryPoints() map[string]func(
	*testService, context.Context, uuid.UUID,
) error {
	return map[string]func(*testService, context.Context, uuid.UUID) error{
		"create pin": func(
			test *testService,
			ctx context.Context,
			id uuid.UUID,
		) error {
			_, err := test.passwordResetService.CreatePIN(ctx, id)

			return err
		},
		"resend": func(
			test *testService,
			ctx context.Context,
			id uuid.UUID,
		) error {
			_, err := test.passwordResetService.ResendVerification(ctx, id)

			return err
		},
	}
}

// A request within both budgets issues a code, sends a message, and spends the
// per-address budget against the address in its canonical form.
//
// The canonical form is what makes the budget shared rather than two budgets
// that happen to have the same name: the address is read from the row, and a
// second spelling of it would key a second allowance.
func TestService_PINEmailBudget_SpendsTheCanonicalAddress(t *testing.T) {
	for name, call := range budgetEntryPoints() {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			ctx := context.Background()
			verificationID := uuid.New()

			const stored = "  Alice@Example.COM  "

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(budgetVerification(verificationID, stored), nil)
			test.repository.EXPECT().
				IssueVerificationCode(ctx, gomock.Any()).
				Return(
					passwordresetdb.VerificationRequest{ID: verificationID},
					nil,
				)

			err := call(test, ctx, verificationID)
			require.NoError(t, err)

			require.Equal(t, 1, test.pinEmailBudget.PerEmailCount())
			require.Equal(
				t,
				[]string{registration.NormalizeEmail(stored)},
				test.pinEmailBudget.PerEmailSubjects,
			)

			require.Equal(t, 1, test.pinEmailBudget.GlobalCount())
			require.Len(t, test.emailSender.Messages, 1)
		})
	}
}

// An exhausted per-address budget stops everything that costs money: no code is
// issued, no message goes out, and the deployment-wide budget is untouched.
//
// The repository expectation for IssueVerificationCode is deliberately absent —
// gomock fails the test if the service reaches it, which is the assertion that no
// code row was written and so that pin_issued_count did not move either.
func TestService_PINEmailBudget_PerEmailExceededIssuesNothing(t *testing.T) {
	ctx := context.Background()

	for name, call := range budgetEntryPoints() {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			test.pinEmailBudget.PerEmail = testutil.PINEmailBudgetOutcome{
				Allowed:    false,
				RetryAfter: 42 * time.Second,
			}

			verificationID := uuid.New()

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(
					budgetVerification(verificationID, "alice@example.com"),
					nil,
				)

			err := call(test, ctx, verificationID)
			require.Error(t, err)

			appErr := apperror.FromError(err)
			require.Equal(t, http.StatusTooManyRequests, appErr.Status)
			require.Equal(t, passwordreset.CodePinRateLimitExceeded, appErr.Code)
			require.NotNil(t, appErr.RetryAfterSeconds())
			require.Equal(t, 42, *appErr.RetryAfterSeconds())

			require.Empty(t, test.emailSender.Messages)
			require.Zero(
				t,
				test.pinEmailBudget.GlobalCount(),
				"a per-address refusal must not spend the deployment budget",
			)
		})
	}
}

// An exhausted deployment-wide budget is a different answer: a client can wait
// out its own address, and can do nothing at all about this one.
func TestService_PINEmailBudget_GlobalExceededReportsItsOwnCode(t *testing.T) {
	ctx := context.Background()

	for name, call := range budgetEntryPoints() {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			test.pinEmailBudget.Global = testutil.PINEmailBudgetOutcome{
				Allowed:    false,
				RetryAfter: 7 * time.Minute,
			}

			verificationID := uuid.New()

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(
					budgetVerification(verificationID, "alice@example.com"),
					nil,
				)

			err := call(test, ctx, verificationID)
			require.Error(t, err)

			appErr := apperror.FromError(err)
			require.Equal(t, http.StatusTooManyRequests, appErr.Status)
			require.Equal(t, passwordreset.CodePINEmailBudgetExceeded, appErr.Code)
			require.Equal(t, 420, *appErr.RetryAfterSeconds())

			require.Equal(t, 1, test.pinEmailBudget.PerEmailCount())
			require.Zero(
				t,
				test.pinEmailBudget.RefundCount(),
				"a refused reservation was never an admission to give back",
			)
			require.Empty(t, test.emailSender.Messages)
		})
	}
}

// A budget that could not be evaluated has not said the caller is within budget.
// Allowing on failure would let anyone remove the ceiling by causing the failure
// that produces it.
func TestService_PINEmailBudget_UnavailableRefusesAndIssuesNothing(t *testing.T) {
	ctx := context.Background()
	redisDown := errors.New("dial tcp: connection refused")

	cases := map[string]struct {
		prepare func(*testutil.CountingPINEmailBudget)
		code    string
	}{
		"per address": {
			prepare: func(b *testutil.CountingPINEmailBudget) {
				b.PerEmail.Err = redisDown
			},
			code: passwordreset.CodePinRateLimitUnavailable,
		},
		"global": {
			prepare: func(b *testutil.CountingPINEmailBudget) {
				b.Global.Err = redisDown
			},
			code: passwordreset.CodePINEmailBudgetUnavailable,
		},
	}

	for budgetName, budgetCase := range cases {
		for name, call := range budgetEntryPoints() {
			t.Run(budgetName+" "+name, func(t *testing.T) {
				test := newTestService(t)
				budgetCase.prepare(test.pinEmailBudget)

				verificationID := uuid.New()

				test.repository.EXPECT().
					GetVerification(ctx, verificationID).
					Return(
						budgetVerification(verificationID, "alice@example.com"),
						nil,
					)

				err := call(test, ctx, verificationID)
				require.Error(t, err)

				appErr := apperror.FromError(err)
				require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
				require.Equal(t, budgetCase.code, appErr.Code)
				require.Nil(
					t,
					appErr.RetryAfterSeconds(),
					"an unevaluated budget has no window to name",
				)

				require.Empty(t, test.emailSender.Messages)
			})
		}
	}
}

// The cooldown is checked before either budget. A caller already being told to
// wait must not also spend one of ten daily admissions it cannot get back until
// the window moves.
func TestService_PINEmailBudget_CooldownRefusalSpendsNeitherBudget(t *testing.T) {
	ctx := context.Background()

	for name, call := range budgetEntryPoints() {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			verificationID := uuid.New()

			verification := budgetVerification(
				verificationID,
				"alice@example.com",
			)
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
				passwordreset.CodeVerificationResendCooldown,
				apperror.FromError(err).Code,
			)

			require.Zero(t, test.pinEmailBudget.PerEmailCount())
			require.Zero(t, test.pinEmailBudget.GlobalCount())
			require.Empty(t, test.emailSender.Messages)
		})
	}
}

// A reservation whose issuing transaction fails is given back. Nothing has been
// sent at that point, so the slot is provably unused, and keeping it would mean
// one broken row cost the deployment a day's worth of allowance.
func TestService_PINEmailBudget_DatabaseFailureRefundsTheGlobalReservation(
	t *testing.T,
) {
	ctx := context.Background()

	for name, call := range budgetEntryPoints() {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			verificationID := uuid.New()

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(
					budgetVerification(verificationID, "alice@example.com"),
					nil,
				)
			test.repository.EXPECT().
				IssueVerificationCode(ctx, gomock.Any()).
				Return(
					passwordresetdb.VerificationRequest{},
					apperror.Internal(errors.New("deadlock detected")),
				)

			err := call(test, ctx, verificationID)
			require.Error(t, err)

			require.Equal(t, 1, test.pinEmailBudget.RefundCount())
			require.Empty(t, test.emailSender.Messages)
		})
	}
}

// A sender that reached a provider and then failed keeps its slot. A timeout does
// not prove the message was declined, and refunding it would hand back capacity
// a message may already have been delivered against.
func TestService_PINEmailBudget_AttemptedSendFailureKeepsTheGlobalReservation(
	t *testing.T,
) {
	ctx := context.Background()

	for name, call := range budgetEntryPoints() {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			test.emailSender.Err = errors.New(
				"read: connection reset by peer",
			)

			verificationID := uuid.New()

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(
					budgetVerification(verificationID, "alice@example.com"),
					nil,
				)
			test.repository.EXPECT().
				IssueVerificationCode(ctx, gomock.Any()).
				Return(
					passwordresetdb.VerificationRequest{ID: verificationID},
					nil,
				)

			err := call(test, ctx, verificationID)
			require.Error(t, err)

			require.Zero(
				t,
				test.pinEmailBudget.RefundCount(),
				"an attempted send keeps its slot whatever it returns",
			)

			appErr := apperror.FromError(err)
			require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
			require.Equal(t, passwordreset.CodePINEmailDeliveryFailed, appErr.Code)
			require.NotNil(
				t,
				appErr.RetryAfterSeconds(),
				"a delivery failure must say when to come back",
			)
		})
	}
}

// A sender that never tried gives its slot back. This is the state the production
// UnconfiguredSender is in: it makes no network call, so nothing could have been
// accepted anywhere, and a staging deployment must not exhaust a real provider's
// budget by failing to send.
func TestService_PINEmailBudget_SenderThatNeverTriedRefundsTheReservation(
	t *testing.T,
) {
	ctx := context.Background()

	for name, call := range budgetEntryPoints() {
		t.Run(name, func(t *testing.T) {
			test := newTestService(t)
			test.emailSender.Err = email.NotAttempted(
				errors.New("email sender is not configured"),
			)

			verificationID := uuid.New()

			test.repository.EXPECT().
				GetVerification(ctx, verificationID).
				Return(
					budgetVerification(verificationID, "alice@example.com"),
					nil,
				)
			test.repository.EXPECT().
				IssueVerificationCode(ctx, gomock.Any()).
				Return(
					passwordresetdb.VerificationRequest{ID: verificationID},
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

// The message is handed to the sender exactly once, through the budget, so there
// is no path that sends without spending and no path that spends twice.
func TestService_PINEmailBudget_DeliversThroughTheSenderOnce(t *testing.T) {
	ctx := context.Background()
	verificationID := uuid.New()
	test := newTestService(t)

	test.repository.EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			budgetVerification(verificationID, "alice@example.com"),
			nil,
		)
	test.repository.EXPECT().
		IssueVerificationCode(ctx, gomock.Any()).
		Return(passwordresetdb.VerificationRequest{ID: verificationID}, nil)

	_, err := test.passwordResetService.CreatePIN(ctx, verificationID)
	require.NoError(t, err)

	require.Len(t, test.emailSender.Messages, 1)
	require.Equal(
		t,
		1,
		test.pinEmailBudget.Delivered,
		"the message reaches the sender exactly once",
	)
	require.Equal(t, 0, test.pinEmailBudget.RefundCount())
}
