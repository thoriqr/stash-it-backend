package registration_test

import (
	"context"
	"errors"
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

func TestRegistrationService_GetVerification(t *testing.T) {
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

func TestRegistrationService_GetVerification_LastSentAtUnavailable(t *testing.T) {
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

func TestRegistrationService_GetVerification_RepositoryError(t *testing.T) {
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