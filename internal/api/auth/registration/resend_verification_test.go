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
func TestService_ResendVerification(t *testing.T) {
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
	lastSentAt := testStartedAt.Add(-2 * time.Minute)

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

	repository.
		EXPECT().
		ResendVerification(
			ctx,
			gomock.Any(),
		).
		DoAndReturn(func(
			_ context.Context,
			params registration.ResendVerificationParams,
		) (registrationdb.VerificationRequest, error) {
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

			return registrationdb.VerificationRequest{
				ID: verificationID,
			}, nil
		})

	result, err := service.ResendVerification(
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
}

func TestService_ResendVerification_Cooldown(t *testing.T) {
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
					Time:  time.Now().Add(-30 * time.Second),
					Valid: true,
				},
			},
			nil,
		)

	_, err := service.ResendVerification(ctx, verificationID)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	appErr := apperror.FromError(err)

	if appErr.Code != registration.CodeVerificationResendCooldown {
		t.Errorf(
			"expected error code %q, got %q",
			registration.CodeVerificationResendCooldown,
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

func TestService_ResendVerification_GetVerificationError(t *testing.T) {
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

	_, err := service.ResendVerification(ctx, verificationID)

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

func TestService_ResendVerification_RepositoryError(t *testing.T) {
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

	repositoryErr := apperror.Internal(
		errors.New("database connection failed"),
	)

	repository.
		EXPECT().
		ResendVerification(
			ctx,
			gomock.Any(),
		).
		Return(
			registrationdb.VerificationRequest{},
			repositoryErr,
		)

	_, err := service.ResendVerification(ctx, verificationID)

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