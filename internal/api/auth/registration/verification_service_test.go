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
	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func TestService_GetVerification(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := registration.NewService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	verificationID := uuid.New()

	testStartedAt := time.Now()
	registrationExpiresAt := testStartedAt.Add(7 * 24 * time.Hour)
	lastSentAt := testStartedAt

	repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{
				ID:                 verificationID,
				Status:             string(registration.VerificationRequestPending),
				RegistrationStatus: string(registration.PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  registrationExpiresAt,
					Valid: true,
				},
				LastSentAt: pgtype.Timestamptz{
					Time:  lastSentAt,
					Valid: true,
				},
			},
			nil,
		)

	result, err := service.GetVerification(ctx, verificationID)
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

	if result.Status != registration.VerificationRequestPending {
		t.Errorf(
			"expected status %q, got %q",
			registration.VerificationRequestPending,
			result.Status,
		)
	}

	if !result.RegistrationExpiresAt.Equal(registrationExpiresAt) {
		t.Errorf(
			"expected registration expiration %v, got %v",
			registrationExpiresAt,
			result.RegistrationExpiresAt,
		)
	}

	if result.LastSentAt == nil {
		t.Fatal("expected last sent at to be set")
	}

	if !result.LastSentAt.Equal(lastSentAt) {
		t.Errorf(
			"expected last sent at %v, got %v",
			lastSentAt,
			*result.LastSentAt,
		)
	}
}

func TestService_GetVerification_ValidationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := registration.NewService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	verificationID := uuid.New()

	repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{
				ID:                 verificationID,
				Status:             string(registration.VerificationRequestVerified),
				RegistrationStatus: string(registration.PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(7 * 24 * time.Hour),
					Valid: true,
				},
			},
			nil,
		)

	_, err := service.GetVerification(ctx, verificationID)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	appErr := apperror.FromError(err)

	if appErr.Code != registration.CodeVerificationNotPending {
		t.Errorf(
			"expected error code %q, got %q",
			registration.CodeVerificationNotPending,
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

func TestService_GetVerification_LastSentAtUnavailable(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := registration.NewService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	verificationID := uuid.New()

	repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{
				ID:                 verificationID,
				Status:             string(registration.VerificationRequestPending),
				RegistrationStatus: string(registration.PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(7 * 24 * time.Hour),
					Valid: true,
				},
				LastSentAt: pgtype.Timestamptz{
					Valid: false,
				},
			},
			nil,
		)

	result, err := service.GetVerification(ctx, verificationID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.LastSentAt != nil {
		t.Errorf("expected last sent at to be nil, got %v", *result.LastSentAt)
	}
}

func TestService_GetVerification_RepositoryError(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := registration.NewService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	verificationID := uuid.New()

	repositoryErr := apperror.Internal(
		errors.New("database connection failed"),
	)

	repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{},
			repositoryErr,
		)

	_, err := service.GetVerification(ctx, verificationID)
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

func TestService_VerifyRegistration(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := registration.NewService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	verificationID := uuid.New()
	pendingRegistrationID := uuid.New()
	verificationCodeID := uuid.New()

	pin := "123456"
	codeHash := verificationCodeHasher.Hash(pin)

	testStartedAt := time.Now()
	registrationExpiresAt := testStartedAt.Add(7 * 24 * time.Hour)

	var continuationTokenHash string

	repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{
				ID:        verificationID,
				SubjectID: pendingRegistrationID,
				Status:    string(registration.VerificationRequestPending),
				RegistrationStatus: string(
					registration.PendingRegistrationPending,
				),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  registrationExpiresAt,
					Valid: true,
				},
			},
			nil,
		)

	repository.
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
				Attempts: 0,
			},
			nil,
		)

	repository.
		EXPECT().
		CompleteVerification(
			ctx,
			gomock.Any(),
		).
		DoAndReturn(func(
			_ context.Context,
			params registration.CompleteVerificationParams,
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

			if params.PendingRegistrationID != pendingRegistrationID {
				t.Errorf(
					"expected pending registration ID %s, got %s",
					pendingRegistrationID,
					params.PendingRegistrationID,
				)
			}

			if params.TokenHash == "" {
				t.Error("expected token hash to be set")
			}

			if !params.ExpiresAt.Valid {
				t.Error("expected continuation expiration to be valid")
			}

			if !params.ExpiresAt.Time.After(testStartedAt) {
				t.Errorf(
					"expected continuation expiration after test start, got %v",
					params.ExpiresAt.Time,
				)
			}

			continuationTokenHash = params.TokenHash

			return registrationdb.RegistrationContinuation{}, nil
		})

	result, err := service.VerifyRegistration(
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

func TestService_VerifyRegistration_GetVerificationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := registration.NewService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	verificationID := uuid.New()

	repositoryErr := apperror.NotFound(
		errors.New("verification not found"),
	)

	repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{},
			repositoryErr,
		)

	_, err := service.VerifyRegistration(
		ctx,
		verificationID,
		"123456",
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

func TestService_VerifyRegistration_ValidationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := registration.NewService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	verificationID := uuid.New()

	repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{
				ID:                 verificationID,
				Status:             string(registration.VerificationRequestVerified),
				RegistrationStatus: string(registration.PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(7 * 24 * time.Hour),
					Valid: true,
				},
			},
			nil,
		)

	_, err := service.VerifyRegistration(
		ctx,
		verificationID,
		"123456",
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	appErr := apperror.FromError(err)

	if appErr.Code != registration.CodeVerificationNotPending {
		t.Errorf(
			"expected error code %q, got %q",
			registration.CodeVerificationNotPending,
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

func TestService_VerifyRegistration_GetActiveVerificationCodeError(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := registration.NewService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	verificationID := uuid.New()

	repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{
				ID:                 verificationID,
				Status:             string(registration.VerificationRequestPending),
				RegistrationStatus: string(registration.PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(7 * 24 * time.Hour),
					Valid: true,
				},
			},
			nil,
		)

	repositoryErr := apperror.ConflictWith(
		"",
		"verification code is no longer available",
		nil,
	)

	repository.
		EXPECT().
		GetActiveVerificationCode(
			ctx,
			verificationID,
			registration.VerificationCodeMaxAttempts,
		).
		Return(
			registrationdb.VerificationCode{},
			repositoryErr,
		)

	_, err := service.VerifyRegistration(
		ctx,
		verificationID,
		"123456",
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

func TestService_VerifyRegistration_InvalidPIN(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := registration.NewService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	verificationID := uuid.New()
	verificationCodeID := uuid.New()

	correctPIN := "123456"
	codeHash := verificationCodeHasher.Hash(correctPIN)

	repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{
				ID:                 verificationID,
				Status:             string(registration.VerificationRequestPending),
				RegistrationStatus: string(registration.PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(7 * 24 * time.Hour),
					Valid: true,
				},
			},
			nil,
		)

	repository.
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
				Attempts: 0,
			},
			nil,
		)

	repository.
		EXPECT().
		IncrementVerificationCodeAttempts(
			ctx,
			verificationCodeID,
			registration.VerificationCodeMaxAttempts,
		).
		Return(
			int32(1),
			nil,
		)

	_, err := service.VerifyRegistration(
		ctx,
		verificationID,
		"654321",
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	appErr := apperror.FromError(err)

	if appErr.Code != registration.CodeInvalidVerificationCode {
		t.Errorf(
			"expected error code %q, got %q",
			registration.CodeInvalidVerificationCode,
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

func TestService_VerifyRegistration_AttemptsExceeded(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := registration.NewService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	verificationID := uuid.New()
	verificationCodeID := uuid.New()

	codeHash := verificationCodeHasher.Hash("123456")

	repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{
				ID:                 verificationID,
				Status:             string(registration.VerificationRequestPending),
				RegistrationStatus: string(registration.PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(7 * 24 * time.Hour),
					Valid: true,
				},
			},
			nil,
		)

	repository.
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
				Attempts: registration.VerificationCodeMaxAttempts - 1,
			},
			nil,
		)

	repository.
		EXPECT().
		IncrementVerificationCodeAttempts(
			ctx,
			verificationCodeID,
			registration.VerificationCodeMaxAttempts,
		).
		Return(
			registration.VerificationCodeMaxAttempts,
			nil,
		)

	_, err := service.VerifyRegistration(
		ctx,
		verificationID,
		"654321",
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	appErr := apperror.FromError(err)

	if appErr.Code != registration.CodeVerificationCodeAttemptsExceeded {
		t.Errorf(
			"expected error code %q, got %q",
			registration.CodeVerificationCodeAttemptsExceeded,
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

func TestService_VerifyRegistration_IncrementAttemptsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := registration.NewService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	verificationID := uuid.New()
	verificationCodeID := uuid.New()

	codeHash := verificationCodeHasher.Hash("123456")

	repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{
				ID:                 verificationID,
				Status:             string(registration.VerificationRequestPending),
				RegistrationStatus: string(registration.PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(7 * 24 * time.Hour),
					Valid: true,
				},
			},
			nil,
		)

	repository.
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
				Attempts: 0,
			},
			nil,
		)

	repositoryErr := apperror.Internal(
		errors.New("database connection failed"),
	)

	repository.
		EXPECT().
		IncrementVerificationCodeAttempts(
			ctx,
			verificationCodeID,
			registration.VerificationCodeMaxAttempts,
		).
		Return(
			int32(0),
			repositoryErr,
		)

	_, err := service.VerifyRegistration(
		ctx,
		verificationID,
		"654321",
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

func TestService_VerifyRegistration_CompleteVerificationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := registration.NewService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	verificationID := uuid.New()
	pendingRegistrationID := uuid.New()
	verificationCodeID := uuid.New()

	pin := "123456"
	codeHash := verificationCodeHasher.Hash(pin)

	repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{
				ID:                 verificationID,
				SubjectID:          pendingRegistrationID,
				Status:             string(registration.VerificationRequestPending),
				RegistrationStatus: string(registration.PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(7 * 24 * time.Hour),
					Valid: true,
				},
			},
			nil,
		)

	repository.
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
				Attempts: 0,
			},
			nil,
		)

	repositoryErr := apperror.Internal(
		errors.New("database connection failed"),
	)

	repository.
		EXPECT().
		CompleteVerification(
			ctx,
			gomock.Any(),
		).
		Return(
			registrationdb.RegistrationContinuation{},
			repositoryErr,
		)

	_, err := service.VerifyRegistration(
		ctx,
		verificationID,
		pin,
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