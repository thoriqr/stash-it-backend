package password_reset_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"

	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func TestService_CreatePIN(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()
	testEmail := "test@example.com"

	testStartedAt := time.Now()
	passwordResetExpiresAt := testStartedAt.Add(7 * 24 * time.Hour)

	test.repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			passwordresetdb.GetVerificationRow{
				ID:                 verificationID,
				Email:              testEmail,
				Status:             string(passwordreset.VerificationRequestPending),
				PasswordResetStatus: string(passwordreset.PendingPasswordResetPending),
				PasswordResetExpiresAt: pgtype.Timestamptz{
					Time:  passwordResetExpiresAt,
					Valid: true,
				},
			},
			nil,
		)

	test.repository.
		EXPECT().
		IssueVerificationCode(
			ctx,
			gomock.Any(),
		).
		DoAndReturn(func(
			_ context.Context,
			params passwordreset.IssueVerificationCodeParams,
		) (passwordresetdb.VerificationRequest, error) {
			if params.VerificationID != verificationID {
				t.Errorf(
					"expected verification ID %s, got %s",
					verificationID,
					params.VerificationID,
				)
			}

			if params.CodeHash == "" {
				t.Error("expected code hash to be set")
			}

			if !params.CodeExpiresAt.Valid {
				t.Error("expected code expiration to be valid")
			}

			now := time.Now()

			codeMin := testStartedAt.Add(5 * time.Minute)
			codeMax := now.Add(5 * time.Minute)

			if params.CodeExpiresAt.Time.Before(codeMin) ||
				params.CodeExpiresAt.Time.After(codeMax) {
				t.Errorf(
					"unexpected code expiration: %v",
					params.CodeExpiresAt.Time,
				)
			}

			return passwordresetdb.VerificationRequest{
				ID: verificationID,
			}, nil
		})

	result, err := test.passwordResetService.CreatePIN(
		ctx,
		verificationID,
	)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.VerificationID != verificationID {
		t.Errorf(
			"expected verification ID %s, got %s",
			verificationID,
			result.VerificationID,
		)
	}

	if len(test.emailSender.Messages) != 1 {
		t.Fatalf(
			"expected 1 email message, got %d",
			len(test.emailSender.Messages),
		)
	}

	message := test.emailSender.Messages[0]
	if message.To.Email != testEmail {
		t.Errorf(
			"expected recipient %q, got %q",
			testEmail,
			message.To.Email,
		)
	}
}

func TestService_CreatePIN_EmailSenderError(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()
	testEmail := "test@example.com"

	testStartedAt := time.Now()
	passwordResetExpiresAt := testStartedAt.Add(7 * 24 * time.Hour)

	test.repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			passwordresetdb.GetVerificationRow{
				ID:                 verificationID,
				Email:              testEmail,
				Status:             string(passwordreset.VerificationRequestPending),
				PasswordResetStatus: string(passwordreset.PendingPasswordResetPending),
				PasswordResetExpiresAt: pgtype.Timestamptz{
					Time:  passwordResetExpiresAt,
					Valid: true,
				},
			},
			nil,
		)

	test.repository.
		EXPECT().
		IssueVerificationCode(
			ctx,
			gomock.Any(),
		).
		Return(
			passwordresetdb.VerificationRequest{
				ID: verificationID,
			},
			nil,
		)

	emailErr := errors.New("email provider unavailable")
	test.emailSender.Err = emailErr

	_, err := test.passwordResetService.CreatePIN(
		ctx,
		verificationID,
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, emailErr) {
		t.Fatalf(
			"expected email sender error, got %v",
			err,
		)
	}

	appErr := apperror.FromError(err)

	if appErr.Code != apperror.CodeInternal {
		t.Errorf(
			"expected error code %q, got %q",
			apperror.CodeInternal,
			appErr.Code,
		)
	}
}

func TestService_CreatePIN_GetVerificationError(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()

	repositoryErr := apperror.NotFound(
		errors.New("verification not found"),
	)

	test.repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			passwordresetdb.GetVerificationRow{},
			repositoryErr,
		)

	_, err := test.passwordResetService.CreatePIN(
		ctx,
		verificationID,
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, repositoryErr) {
		t.Fatalf(
			"expected repository error, got %v",
			err,
		)
	}
}

func TestService_CreatePIN_PasswordResetExpired(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()

	test.repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			passwordresetdb.GetVerificationRow{
				ID:                 verificationID,
				Status:             string(passwordreset.VerificationRequestPending),
				PasswordResetStatus: string(passwordreset.PendingPasswordResetPending),
				PasswordResetExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(-time.Hour),
					Valid: true,
				},
			},
			nil,
		)

	_, err := test.passwordResetService.CreatePIN(
		ctx,
		verificationID,
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	appErr := apperror.FromError(err)

	if appErr.Code != passwordreset.CodePasswordResetExpired {
		t.Errorf(
			"expected error code %q, got %q",
			passwordreset.CodePasswordResetExpired,
			appErr.Code,
		)
	}

	if appErr.Status != http.StatusConflict {
		t.Errorf(
			"expected status %d, got %d",
			http.StatusConflict,
			appErr.Status,
		)
	}
}

func TestService_CreatePIN_VerificationNotPending(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()

	test.repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			passwordresetdb.GetVerificationRow{
				ID:                   verificationID,
				Status:               string(passwordreset.VerificationRequestVerified),
				PasswordResetStatus:  string(passwordreset.PendingPasswordResetPending),
				PasswordResetExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(time.Hour),
					Valid: true,
				},
			},
			nil,
		)

	_, err := test.passwordResetService.CreatePIN(
		ctx,
		verificationID,
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	appErr := apperror.FromError(err)

	if appErr.Code != passwordreset.CodeVerificationNotPending {
		t.Errorf(
			"expected error code %q, got %q",
			passwordreset.CodeVerificationNotPending,
			appErr.Code,
		)
	}

	if appErr.Status != http.StatusConflict {
		t.Errorf(
			"expected status %d, got %d",
			http.StatusConflict,
			appErr.Status,
		)
	}
}

func TestService_CreatePIN_PasswordResetNotPending(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()

	test.repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			passwordresetdb.GetVerificationRow{
				ID:                   verificationID,
				Status:               string(passwordreset.VerificationRequestPending),
				PasswordResetStatus:  string(passwordreset.PendingPasswordResetCompleted),
				PasswordResetExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(time.Hour),
					Valid: true,
				},
			},
			nil,
		)

	_, err := test.passwordResetService.CreatePIN(
		ctx,
		verificationID,
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	appErr := apperror.FromError(err)

	if appErr.Code != passwordreset.CodePasswordResetNotPending {
		t.Errorf(
			"expected error code %q, got %q",
			passwordreset.CodePasswordResetNotPending,
			appErr.Code,
		)
	}

	if appErr.Status != http.StatusConflict {
		t.Errorf(
			"expected status %d, got %d",
			http.StatusConflict,
			appErr.Status,
		)
	}
}

func TestService_CreatePIN_IssueVerificationCodeError(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()

	test.repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			passwordresetdb.GetVerificationRow{
				ID:                   verificationID,
				Status:               string(passwordreset.VerificationRequestPending),
				PasswordResetStatus:  string(passwordreset.PendingPasswordResetPending),
				PasswordResetExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(time.Hour),
					Valid: true,
				},
			},
			nil,
		)

	repositoryErr := apperror.Internal(
		errors.New("database connection failed"),
	)

	test.repository.
		EXPECT().
		IssueVerificationCode(
			ctx,
			gomock.Any(),
		).
		Return(
			passwordresetdb.VerificationRequest{},
			repositoryErr,
		)

	_, err := test.passwordResetService.CreatePIN(
		ctx,
		verificationID,
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, repositoryErr) {
		t.Fatalf(
			"expected repository error, got %v",
			err,
		)
	}
}