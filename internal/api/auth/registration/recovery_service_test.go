package registration_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	registrationdb "github.com/thoriqr/stash-it-backend/internal/api/auth/registration/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func TestService_InitiateManualRecovery(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()

	test.repository.
		EXPECT().
		GetCompletedRegistrationByEmail(
			ctx,
			"test@example.com",
		).
		Return(
			uuid.Nil,
			nil,
		)

	test.repository.
		EXPECT().
		InitiateManualRecovery(
			ctx,
			gomock.Any(),
		).
		DoAndReturn(func(
			_ context.Context,
			params registration.InitiateManualRecoveryParams,
		) (registration.InitiateManualRecoveryResult, error) {
			if params.Email != "test@example.com" {
				t.Errorf(
					"expected normalized email %q, got %q",
					"test@example.com",
					params.Email,
				)
			}

			return registration.InitiateManualRecoveryResult{
				VerificationRequest: registrationdb.VerificationRequest{
					ID:     verificationID,
					Status: string(registration.VerificationRequestPending),
				},
			}, nil
		})

	result, err := test.registrationService.InitiateManualRecovery(
		ctx,
		"  Test@Example.COM  ",
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

	if result.VerificationStatus != registration.VerificationRequestPending {
		t.Errorf(
			"expected verification status %q, got %q",
			registration.VerificationRequestPending,
			result.VerificationStatus,
		)
	}

	if result.AlreadyPending {
		t.Error("expected AlreadyPending to be false")
	}
}

func TestService_InitiateManualRecovery_AlreadyPending(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()

	test.repository.
		EXPECT().
		GetCompletedRegistrationByEmail(
			ctx,
			"test@example.com",
		).
		Return(
			uuid.Nil,
			nil,
		)

	test.repository.
		EXPECT().
		InitiateManualRecovery(
			ctx,
			gomock.Any(),
		).
		Return(
			registration.InitiateManualRecoveryResult{
				VerificationRequest: registrationdb.VerificationRequest{
					ID:     verificationID,
					Status: string(registration.VerificationRequestPending),
				},
				AlreadyPending: true,
			},
			nil,
		)

	result, err := test.registrationService.InitiateManualRecovery(
		ctx,
		"test@example.com",
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

	if !result.AlreadyPending {
		t.Error("expected AlreadyPending to be true")
	}
}

func TestService_InitiateManualRecovery_AlreadyCompleted(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	registrationID := uuid.New()

	test.repository.
		EXPECT().
		GetCompletedRegistrationByEmail(
			ctx,
			"test@example.com",
		).
		Return(
			registrationID,
			nil,
		)

	_, err := test.registrationService.InitiateManualRecovery(
		ctx,
		"test@example.com",
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	appErr := apperror.FromError(err)

	if appErr.Code != registration.CodeRegistrationAlreadyCompleted {
		t.Errorf(
			"expected code %q, got %q",
			registration.CodeRegistrationAlreadyCompleted,
			appErr.Code,
		)
	}
}

// Eligibility is decided by the repository, against the locked registration
// row. The service must not rewrite that refusal: a registration whose normal
// verification is still pending is a conflict for the caller, not a server
// fault and not a success carrying a verification id.
func TestService_InitiateManualRecovery_PendingNormalVerificationRejected(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()

	test.repository.
		EXPECT().
		GetCompletedRegistrationByEmail(
			ctx,
			"test@example.com",
		).
		Return(
			uuid.Nil,
			nil,
		)

	test.repository.
		EXPECT().
		InitiateManualRecovery(
			ctx,
			gomock.Any(),
		).
		Return(
			registration.InitiateManualRecoveryResult{},
			apperror.ConflictWith(
				registration.CodeRegistrationAlreadyPending,
				"registration verification is still pending",
				nil,
			),
		)

	result, err := test.registrationService.InitiateManualRecovery(
		ctx,
		"test@example.com",
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if result.VerificationID != uuid.Nil {
		t.Errorf(
			"expected no verification ID on a refusal, got %s",
			result.VerificationID,
		)
	}

	appErr := apperror.FromError(err)

	if appErr.Code != registration.CodeRegistrationAlreadyPending {
		t.Errorf(
			"expected code %q, got %q",
			registration.CodeRegistrationAlreadyPending,
			appErr.Code,
		)
	}

	if appErr.Status != http.StatusConflict {
		t.Errorf(
			"expected HTTP status %d, got %d",
			http.StatusConflict,
			appErr.Status,
		)
	}
}

func TestService_InitiateManualRecovery_RepositoryError(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	repositoryErr := apperror.Internal(
		errors.New("database connection failed"),
	)

	test.repository.
		EXPECT().
		GetCompletedRegistrationByEmail(
			ctx,
			"test@example.com",
		).
		Return(
			uuid.Nil,
			nil,
		)

	test.repository.
		EXPECT().
		InitiateManualRecovery(
			ctx,
			gomock.Any(),
		).
		Return(
			registration.InitiateManualRecoveryResult{},
			repositoryErr,
		)

	_, err := test.registrationService.InitiateManualRecovery(
		ctx,
		"test@example.com",
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, repositoryErr) {
		t.Fatalf("expected repository error, got %v", err)
	}
}

func TestService_VerifyRegistration_Recovery(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()
	oldRegistrationID := uuid.New()
	verificationCodeID := uuid.New()

	pin := "123456"
	codeHash := test.verificationCodeHasher.Hash(pin)

	testStartedAt := time.Now()

	test.repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{
				ID:        verificationID,
				SubjectID: oldRegistrationID,
				Purpose: string(
					registration.VerificationPurposeRegistrationRecovery,
				),
				Status: string(registration.VerificationRequestPending),
				RegistrationStatus: string(
					registration.PendingRegistrationPending,
				),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  testStartedAt.Add(7 * 24 * time.Hour),
					Valid: true,
				},
			},
			nil,
		)

	test.repository.
		EXPECT().
		GetActiveVerificationCode(
			ctx,
			verificationID,
			registration.VerificationCodeMaxAttempts,
		).
		Return(
			registrationdb.VerificationCode{
				ID:       verificationCodeID,
				CodeHash: codeHash,
			},
			nil,
		)

	var continuationTokenHash string

	test.repository.
		EXPECT().
		CompleteRecoveryVerification(
			ctx,
			gomock.Any(),
		).
		DoAndReturn(func(
			_ context.Context,
			params registration.CompleteRecoveryVerificationParams,
		) (registrationdb.RegistrationContinuation, error) {
			if params.VerificationCodeID != verificationCodeID {
				t.Errorf(
					"expected verification code ID %s, got %s",
					verificationCodeID,
					params.VerificationCodeID,
				)
			}

			if params.VerificationRequestID != verificationID {
				t.Errorf(
					"expected verification request ID %s, got %s",
					verificationID,
					params.VerificationRequestID,
				)
			}

			if params.OldPendingRegistrationID != oldRegistrationID {
				t.Errorf(
					"expected old registration ID %s, got %s",
					oldRegistrationID,
					params.OldPendingRegistrationID,
				)
			}

			if params.TokenHash == "" {
				t.Error("expected token hash to be set")
			}

			if !params.ContinuationExpiresAt.Valid ||
				!params.ContinuationExpiresAt.Time.After(testStartedAt) {
				t.Error("expected a future continuation expiration")
			}

			if !params.NewRegistrationExpiresAt.Valid ||
				!params.NewRegistrationExpiresAt.Time.After(testStartedAt) {
				t.Error("expected a future registration expiration")
			}

			continuationTokenHash = params.TokenHash

			return registrationdb.RegistrationContinuation{}, nil
		})

	result, err := test.registrationService.VerifyRegistration(
		ctx,
		verificationID,
		pin,
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.RegistrationContinuationToken == "" {
		t.Fatal("expected registration continuation token to be set")
	}

	expectedTokenHash := security.HashToken(
		result.RegistrationContinuationToken,
	)

	if continuationTokenHash != expectedTokenHash {
		t.Errorf(
			"expected continuation token hash %s, got %s",
			expectedTokenHash,
			continuationTokenHash,
		)
	}
}
