package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	auth "github.com/thoriqr/stash-it-backend/internal/auth"
	authdb "github.com/thoriqr/stash-it-backend/internal/auth/generated"
	"github.com/thoriqr/stash-it-backend/internal/auth/mocks"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func TestRegistrationService_RegisterManual(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRegistrationRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := auth.NewRegistrationService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	verificationID := uuid.New()

	testStartedAt := time.Now()

	repository.
		EXPECT().
		GetPendingRegistrationByEmail(
			ctx,
			"test@example.com",
		).
		Return(
			authdb.GetPendingRegistrationByEmailRow{},
			false,
			nil,
		)

	repository.
		EXPECT().
		CreateManualRegistration(
			ctx,
			gomock.Any(),
		).
		DoAndReturn(func(
			_ context.Context,
			params auth.CreateManualRegistrationParams,
		) (authdb.VerificationRequest, error) {
			if params.Email != "test@example.com" {
				t.Errorf(
					"expected normalized email %q, got %q",
					"test@example.com",
					params.Email,
				)
			}

			if !params.RegistrationExpiresAt.Valid {
				t.Error("expected registration expiration to be valid")
			}

			if !params.CodeExpiresAt.Valid {
				t.Error("expected code expiration to be valid")
			}

			if params.CodeHash == "" {
				t.Error("expected code hash to be set")
			}

			now := time.Now()

			registrationMin := testStartedAt.Add(7 * 24 * time.Hour)
			registrationMax := now.Add(7 * 24 * time.Hour)

			if params.RegistrationExpiresAt.Time.Before(registrationMin) ||
				params.RegistrationExpiresAt.Time.After(registrationMax) {
				t.Errorf(
					"unexpected registration expiration: %v",
					params.RegistrationExpiresAt.Time,
				)
			}

			codeMin := testStartedAt.Add(5 * time.Minute)
			codeMax := now.Add(5 * time.Minute)

			if params.CodeExpiresAt.Time.Before(codeMin) ||
				params.CodeExpiresAt.Time.After(codeMax) {
				t.Errorf(
					"unexpected code expiration: %v",
					params.CodeExpiresAt.Time,
				)
			}

			return authdb.VerificationRequest{
				ID: verificationID,
			}, nil
		})

	result, err := service.RegisterManual(
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

	if result.AlreadyPending {
		t.Error("expected AlreadyPending to be false")
	}
}

func TestRegistrationService_RegisterManual_AlreadyPending(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRegistrationRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := auth.NewRegistrationService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	verificationID := uuid.New()

	repository.
		EXPECT().
		GetPendingRegistrationByEmail(
			ctx,
			"test@example.com",
		).
		Return(
			authdb.GetPendingRegistrationByEmailRow{
				VerificationID: verificationID,
			},
			true,
			nil,
		)

	result, err := service.RegisterManual(
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

	if !result.AlreadyPending {
		t.Error("expected AlreadyPending to be true")
	}
}

func TestRegistrationService_RegisterManual_RepositoryError(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRegistrationRepository(ctrl)

	passwordHasher := security.NewPasswordHasher()
	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte("test-secret"),
	)

	service := auth.NewRegistrationService(
		repository,
		passwordHasher,
		verificationCodeHasher,
	)

	ctx := context.Background()
	repositoryErr := apperror.Internal(errors.New("database connection failed"))

	repository.
		EXPECT().
		GetPendingRegistrationByEmail(
			ctx,
			"test@example.com",
		).
		Return(
			authdb.GetPendingRegistrationByEmailRow{},
			false,
			repositoryErr,
		)

	_, err := service.RegisterManual(
		ctx,
		"  Test@Example.COM  ",
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