package registration_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	registrationdb "github.com/thoriqr/stash-it-backend/internal/api/auth/registration/generated"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func TestService_GetRegistrationContinuation(t *testing.T) {
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
	token := "test-continuation-token"
	tokenHash := security.HashToken(token)

	testStartedAt := time.Now()
	expiresAt := testStartedAt.Add(15 * time.Minute)

	repository.
		EXPECT().
		GetRegistrationContinuation(ctx, tokenHash).
		Return(
			registrationdb.GetRegistrationContinuationRow{
				Email:            "user@example.com",
				RegistrationType: string(registration.RegistrationTypeManual),
				ExpiresAt: pgtype.Timestamptz{
					Time:  expiresAt,
					Valid: true,
				},
				ConsumedAt: pgtype.Timestamptz{
					Valid: false,
				},
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

	result, err := service.GetRegistrationContinuation(ctx, token)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.Email != "user@example.com" {
		t.Errorf(
			"expected email %q, got %q",
			"user@example.com",
			result.Email,
		)
	}

	if result.RegistrationType != registration.RegistrationTypeManual {
		t.Errorf(
			"expected registration type %q, got %q",
			registration.RegistrationTypeManual,
			result.RegistrationType,
		)
	}

	if !result.RequiresPassword {
		t.Error("expected requires password to be true")
	}

	if !result.ExpiresAt.Equal(expiresAt) {
		t.Errorf(
			"expected expiration %v, got %v",
			expiresAt,
			result.ExpiresAt,
		)
	}
}

func TestService_GetRegistrationContinuation_GetRegistrationContinuationError(t *testing.T) {
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
	token := "test-continuation-token"
	tokenHash := security.HashToken(token)

	repositoryErr := apperror.ConflictWith(
		"",
		"registration continuation is no longer available",
		nil,
	)

	repository.
		EXPECT().
		GetRegistrationContinuation(ctx, tokenHash).
		Return(
			registrationdb.GetRegistrationContinuationRow{},
			repositoryErr,
		)

	_, err := service.GetRegistrationContinuation(ctx, token)
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

func TestService_GetRegistrationContinuation_ValidationError(t *testing.T) {
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
	token := "test-continuation-token"
	tokenHash := security.HashToken(token)

	repository.
		EXPECT().
		GetRegistrationContinuation(ctx, tokenHash).
		Return(
			registrationdb.GetRegistrationContinuationRow{
				ConsumedAt: pgtype.Timestamptz{
					Time:  time.Now(),
					Valid: true,
				},
				ExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(15 * time.Minute),
					Valid: true,
				},
				RegistrationStatus: string(
					registration.PendingRegistrationPending,
				),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(7 * 24 * time.Hour),
					Valid: true,
				},
			},
			nil,
		)

	_, err := service.GetRegistrationContinuation(ctx, token)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	appErr := apperror.FromError(err)

	if appErr.Code != registration.CodeRegistrationContinuationConsumed {
		t.Errorf(
			"expected error code %q, got %q",
			registration.CodeRegistrationContinuationConsumed,
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