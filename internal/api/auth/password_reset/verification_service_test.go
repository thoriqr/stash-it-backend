package password_reset_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"

	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func TestService_GetVerification(t *testing.T) {
	ctx := context.Background()

	t.Run("verification not pending", func(t *testing.T) {
		test := newTestService(t)

		id := uuid.New()

		test.repository.
			EXPECT().
			GetVerification(ctx, id).
			Return(
				passwordresetdb.GetVerificationRow{
					Status: "verified",
				},
				nil,
			)

		_, err := test.passwordResetService.GetVerification(ctx, id)

		if got := apperror.FromError(err).Code; got != passwordreset.CodeVerificationNotPending {
			t.Fatalf(
				"expected %s, got %s",
				passwordreset.CodeVerificationNotPending,
				got,
			)
		}
	})

	t.Run("reset not pending", func(t *testing.T) {
		test := newTestService(t)

		id := uuid.New()

		test.repository.
			EXPECT().
			GetVerification(ctx, id).
			Return(
				passwordresetdb.GetVerificationRow{
					Status: string(passwordreset.VerificationRequestPending),
					PasswordResetStatus: string(
						passwordreset.PendingPasswordResetCompleted,
					),
				},
				nil,
			)

		_, err := test.passwordResetService.GetVerification(ctx, id)

		if got := apperror.FromError(err).Code; got != passwordreset.CodePasswordResetNotPending {
			t.Fatalf(
				"expected %s, got %s",
				passwordreset.CodePasswordResetNotPending,
				got,
			)
		}
	})

	t.Run("reset expired", func(t *testing.T) {
		test := newTestService(t)

		id := uuid.New()

		test.repository.
			EXPECT().
			GetVerification(ctx, id).
			Return(
				passwordresetdb.GetVerificationRow{
					Status: string(passwordreset.VerificationRequestPending),
					PasswordResetStatus: string(
						passwordreset.PendingPasswordResetPending,
					),
					PasswordResetExpiresAt: pgtype.Timestamptz{
						Time:  time.Now().Add(-time.Minute),
						Valid: true,
					},
				},
				nil,
			)

		_, err := test.passwordResetService.GetVerification(ctx, id)

		if got := apperror.FromError(err).Code; got != passwordreset.CodePasswordResetExpired {
			t.Fatalf(
				"expected %s, got %s",
				passwordreset.CodePasswordResetExpired,
				got,
			)
		}
	})

	t.Run("success", func(t *testing.T) {
		test := newTestService(t)

		id := uuid.New()

		row := pendingVerification(id, uuid.New())
		row.LastSentAt = pgtype.Timestamptz{
			Time:  time.Now(),
			Valid: true,
		}

		test.repository.
			EXPECT().
			GetVerification(ctx, id).
			Return(row, nil)

		result, err := test.passwordResetService.GetVerification(ctx, id)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if result.LastSentAt == nil {
			t.Fatal("expected last sent at")
		}

		if result.VerificationID != id {
			t.Errorf(
				"expected verification ID %s, got %s",
				id,
				result.VerificationID,
			)
		}
	})
}

func TestService_VerifyPasswordReset(t *testing.T) {
	ctx := context.Background()

	t.Run("active code lookup error", func(t *testing.T) {
		test := newTestService(t)

		verificationID := uuid.New()
		want := errors.New("code lookup")

		test.repository.
			EXPECT().
			GetVerification(ctx, verificationID).
			Return(
				pendingVerification(verificationID, uuid.New()),
				nil,
			)

		test.repository.
			EXPECT().
			GetActiveVerificationCode(
				ctx,
				verificationID,
				passwordreset.VerificationCodeMaxAttempts,
			).
			Return(
				passwordresetdb.VerificationCode{},
				want,
			)

		_, err := test.passwordResetService.VerifyPasswordReset(
			ctx,
			verificationID,
			"123456",
		)

		if !errors.Is(err, want) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("invalid PIN increments attempts", func(t *testing.T) {
		test := newTestService(t)

		verificationID := uuid.New()
		codeID := uuid.New()
		resetID := uuid.New()

		test.repository.
			EXPECT().
			GetVerification(ctx, verificationID).
			Return(
				pendingVerification(verificationID, resetID),
				nil,
			)

		test.repository.
			EXPECT().
			GetActiveVerificationCode(
				ctx,
				verificationID,
				passwordreset.VerificationCodeMaxAttempts,
			).
			Return(
				passwordresetdb.VerificationCode{
					ID: codeID,
					CodeHash: security.NewVerificationCodeHasher(
						[]byte("test-secret"),
					).Hash("123456"),
				},
				nil,
			)

		test.repository.
			EXPECT().
			IncrementVerificationCodeAttempts(
				ctx,
				codeID,
				passwordreset.VerificationCodeMaxAttempts,
			).
			Return(int32(1), nil)

		_, err := test.passwordResetService.VerifyPasswordReset(
			ctx,
			verificationID,
			"000000",
		)

		if got := apperror.FromError(err).Code; got != passwordreset.CodeInvalidVerificationCode {
			t.Fatalf("got %s", got)
		}
	})

	t.Run("attempt limit exceeded", func(t *testing.T) {
		test := newTestService(t)

		verificationID := uuid.New()
		codeID := uuid.New()

		test.repository.
			EXPECT().
			GetVerification(ctx, verificationID).
			Return(
				pendingVerification(verificationID, uuid.New()),
				nil,
			)

		test.repository.
			EXPECT().
			GetActiveVerificationCode(
				ctx,
				verificationID,
				passwordreset.VerificationCodeMaxAttempts,
			).
			Return(
				passwordresetdb.VerificationCode{
					ID:       codeID,
					CodeHash: "wrong",
				},
				nil,
			)

		test.repository.
			EXPECT().
			IncrementVerificationCodeAttempts(
				ctx,
				codeID,
				passwordreset.VerificationCodeMaxAttempts,
			).
			Return(
				passwordreset.VerificationCodeMaxAttempts,
				nil,
			)

		_, err := test.passwordResetService.VerifyPasswordReset(
			ctx,
			verificationID,
			"000000",
		)

		if got := apperror.FromError(err).Code; got != passwordreset.CodeVerificationCodeAttemptsExceeded {
			t.Fatalf("got %s", got)
		}
	})

	t.Run("increment error", func(t *testing.T) {
		test := newTestService(t)

		verificationID := uuid.New()
		codeID := uuid.New()
		want := errors.New("increment")

		test.repository.
			EXPECT().
			GetVerification(ctx, verificationID).
			Return(
				pendingVerification(verificationID, uuid.New()),
				nil,
			)

		test.repository.
			EXPECT().
			GetActiveVerificationCode(
				ctx,
				verificationID,
				passwordreset.VerificationCodeMaxAttempts,
			).
			Return(
				passwordresetdb.VerificationCode{
					ID:       codeID,
					CodeHash: "wrong",
				},
				nil,
			)

		test.repository.
			EXPECT().
			IncrementVerificationCodeAttempts(
				ctx,
				codeID,
				passwordreset.VerificationCodeMaxAttempts,
			).
			Return(0, want)

		_, err := test.passwordResetService.VerifyPasswordReset(
			ctx,
			verificationID,
			"000000",
		)

		if !errors.Is(err, want) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("complete error", func(t *testing.T) {
		test := newTestService(t)

		verificationID := uuid.New()
		codeID := uuid.New()
		want := errors.New("complete")

		test.repository.
			EXPECT().
			GetVerification(ctx, verificationID).
			Return(
				pendingVerification(verificationID, uuid.New()),
				nil,
			)

		test.repository.
			EXPECT().
			GetActiveVerificationCode(
				ctx,
				verificationID,
				passwordreset.VerificationCodeMaxAttempts,
			).
			Return(
				passwordresetdb.VerificationCode{
					ID: codeID,
					CodeHash: security.NewVerificationCodeHasher(
						[]byte("test-secret"),
					).Hash("123456"),
				},
				nil,
			)

		test.repository.
			EXPECT().
			CompleteVerification(
				ctx,
				gomock.Any(),
			).
			Return(
				passwordresetdb.PasswordResetContinuation{},
				want,
			)

		_, err := test.passwordResetService.VerifyPasswordReset(
			ctx,
			verificationID,
			"123456",
		)

		if !errors.Is(err, want) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		test := newTestService(t)

		verificationID := uuid.New()
		codeID := uuid.New()
		resetID := uuid.New()

		test.repository.
			EXPECT().
			GetVerification(ctx, verificationID).
			Return(
				pendingVerification(verificationID, resetID),
				nil,
			)

		test.repository.
			EXPECT().
			GetActiveVerificationCode(
				ctx,
				verificationID,
				passwordreset.VerificationCodeMaxAttempts,
			).
			Return(
				passwordresetdb.VerificationCode{
					ID: codeID,
					CodeHash: security.NewVerificationCodeHasher(
						[]byte("test-secret"),
					).Hash("123456"),
				},
				nil,
			)

		test.repository.
			EXPECT().
			CompleteVerification(
				ctx,
				gomock.Any(),
			).
			DoAndReturn(
				func(
					_ context.Context,
					params passwordreset.CompleteVerificationParams,
				) (passwordresetdb.PasswordResetContinuation, error) {
					if params.VerificationCodeID != codeID ||
						params.VerificationRequestID != verificationID ||
						params.PendingPasswordResetID != resetID {
						t.Errorf("unexpected %#v", params)
					}

					return passwordresetdb.PasswordResetContinuation{}, nil
				},
			)

		result, err := test.passwordResetService.VerifyPasswordReset(
			ctx,
			verificationID,
			"123456",
		)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if result.PasswordResetContinuationToken == "" {
			t.Fatal("expected continuation token")
		}
	})
}