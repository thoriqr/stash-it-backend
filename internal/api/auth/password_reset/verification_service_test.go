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
	"github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func TestService_GetVerification(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		row  passwordresetdb.GetVerificationRow
		code string
	}{
		{"verification not pending", passwordresetdb.GetVerificationRow{Status: "verified"}, passwordreset.CodeVerificationNotPending},
		{"reset not pending", passwordresetdb.GetVerificationRow{Status: string(passwordreset.VerificationRequestPending), PasswordResetStatus: string(passwordreset.PendingPasswordResetCompleted)}, passwordreset.CodePasswordResetNotPending},
		{"reset expired", passwordresetdb.GetVerificationRow{Status: string(passwordreset.VerificationRequestPending), PasswordResetStatus: string(passwordreset.PendingPasswordResetPending), PasswordResetExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}}, passwordreset.CodePasswordResetExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			repository := mocks.NewMockRepository(ctrl)
			id := uuid.New()
			repository.EXPECT().GetVerification(ctx, id).Return(tt.row, nil)
			_, err := newService(repository).GetVerification(ctx, id)
			if got := apperror.FromError(err).Code; got != tt.code {
				t.Fatalf("expected %s, got %s", tt.code, got)
			}
		})
	}
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)
	id := uuid.New()
	row := pendingVerification(id, uuid.New())
	row.LastSentAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	repository.EXPECT().GetVerification(ctx, id).Return(row, nil)
	result, err := newService(repository).GetVerification(ctx, id)
	if err != nil || result.LastSentAt == nil || result.VerificationID != id {
		t.Fatalf("unexpected result %#v, err %v", result, err)
	}
}

func TestService_VerifyPasswordReset(t *testing.T) {
	ctx := context.Background()
	t.Run("active code lookup error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		verificationID, want := uuid.New(), errors.New("code lookup")
		repository.EXPECT().GetVerification(ctx, verificationID).Return(pendingVerification(verificationID, uuid.New()), nil)
		repository.EXPECT().GetActiveVerificationCode(ctx, verificationID, passwordreset.VerificationCodeMaxAttempts).Return(passwordresetdb.VerificationCode{}, want)
		_, err := newService(repository).VerifyPasswordReset(ctx, verificationID, "123456")
		if !errors.Is(err, want) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("invalid PIN increments attempts", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		verificationID, codeID, resetID := uuid.New(), uuid.New(), uuid.New()
		repository.EXPECT().GetVerification(ctx, verificationID).Return(pendingVerification(verificationID, resetID), nil)
		repository.EXPECT().GetActiveVerificationCode(ctx, verificationID, passwordreset.VerificationCodeMaxAttempts).Return(passwordresetdb.VerificationCode{ID: codeID, CodeHash: security.NewVerificationCodeHasher([]byte("test-secret")).Hash("123456")}, nil)
		repository.EXPECT().IncrementVerificationCodeAttempts(ctx, codeID, passwordreset.VerificationCodeMaxAttempts).Return(int32(1), nil)
		_, err := newService(repository).VerifyPasswordReset(ctx, verificationID, "000000")
		if got := apperror.FromError(err).Code; got != passwordreset.CodeInvalidVerificationCode {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("attempt limit exceeded", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		verificationID, codeID := uuid.New(), uuid.New()
		repository.EXPECT().GetVerification(ctx, verificationID).Return(pendingVerification(verificationID, uuid.New()), nil)
		repository.EXPECT().GetActiveVerificationCode(ctx, verificationID, passwordreset.VerificationCodeMaxAttempts).Return(passwordresetdb.VerificationCode{ID: codeID, CodeHash: "wrong"}, nil)
		repository.EXPECT().IncrementVerificationCodeAttempts(ctx, codeID, passwordreset.VerificationCodeMaxAttempts).Return(passwordreset.VerificationCodeMaxAttempts, nil)
		_, err := newService(repository).VerifyPasswordReset(ctx, verificationID, "000000")
		if got := apperror.FromError(err).Code; got != passwordreset.CodeVerificationCodeAttemptsExceeded {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("increment error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		verificationID, codeID, want := uuid.New(), uuid.New(), errors.New("increment")
		repository.EXPECT().GetVerification(ctx, verificationID).Return(pendingVerification(verificationID, uuid.New()), nil)
		repository.EXPECT().GetActiveVerificationCode(ctx, verificationID, passwordreset.VerificationCodeMaxAttempts).Return(passwordresetdb.VerificationCode{ID: codeID, CodeHash: "wrong"}, nil)
		repository.EXPECT().IncrementVerificationCodeAttempts(ctx, codeID, passwordreset.VerificationCodeMaxAttempts).Return(0, want)
		_, err := newService(repository).VerifyPasswordReset(ctx, verificationID, "000000")
		if !errors.Is(err, want) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("complete error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		verificationID, codeID, want := uuid.New(), uuid.New(), errors.New("complete")
		repository.EXPECT().GetVerification(ctx, verificationID).Return(pendingVerification(verificationID, uuid.New()), nil)
		repository.EXPECT().GetActiveVerificationCode(ctx, verificationID, passwordreset.VerificationCodeMaxAttempts).Return(passwordresetdb.VerificationCode{ID: codeID, CodeHash: security.NewVerificationCodeHasher([]byte("test-secret")).Hash("123456")}, nil)
		repository.EXPECT().CompleteVerification(ctx, gomock.Any()).Return(passwordresetdb.PasswordResetContinuation{}, want)
		_, err := newService(repository).VerifyPasswordReset(ctx, verificationID, "123456")
		if !errors.Is(err, want) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		verificationID, codeID, resetID := uuid.New(), uuid.New(), uuid.New()
		repository.EXPECT().GetVerification(ctx, verificationID).Return(pendingVerification(verificationID, resetID), nil)
		repository.EXPECT().GetActiveVerificationCode(ctx, verificationID, passwordreset.VerificationCodeMaxAttempts).Return(passwordresetdb.VerificationCode{ID: codeID, CodeHash: security.NewVerificationCodeHasher([]byte("test-secret")).Hash("123456")}, nil)
		repository.EXPECT().CompleteVerification(ctx, gomock.Any()).DoAndReturn(func(_ context.Context, params passwordreset.CompleteVerificationParams) (passwordresetdb.PasswordResetContinuation, error) {
			if params.VerificationCodeID != codeID || params.VerificationRequestID != verificationID || params.PendingPasswordResetID != resetID {
				t.Errorf("unexpected %#v", params)
			}
			return passwordresetdb.PasswordResetContinuation{}, nil
		})
		result, err := newService(repository).VerifyPasswordReset(ctx, verificationID, "123456")
		if err != nil || result.PasswordResetContinuationToken == "" {
			t.Fatalf("unexpected %#v %v", result, err)
		}
	})
}
