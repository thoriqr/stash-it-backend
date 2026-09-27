package registration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	registrationdb "github.com/thoriqr/stash-it-backend/internal/api/auth/registration/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func TestService_RegisterManual(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()

	testStartedAt := time.Now()

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
		CreateManualRegistration(
			ctx,
			gomock.Any(),
		).
		DoAndReturn(func(
			_ context.Context,
			params registration.CreateManualRegistrationParams,
		) (registration.CreateManualRegistrationResult, error) {
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

			return registration.CreateManualRegistrationResult{
				VerificationRequest: registrationdb.VerificationRequest{
					ID: verificationID,
				},
			}, nil
		})

	result, err := test.registrationService.RegisterManual(
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

func TestService_RegisterManual_AlreadyPending(t *testing.T) {
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
		CreateManualRegistration(
			ctx,
			gomock.Any(),
		).
		Return(
			registration.CreateManualRegistrationResult{
				VerificationRequest: registrationdb.VerificationRequest{
					ID: verificationID,
				},
				AlreadyPending: true,
			},
			nil,
		)

	result, err := test.registrationService.RegisterManual(
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

func TestService_RegisterManual_AlreadyCompleted(t *testing.T) {
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

	_, err := test.registrationService.RegisterManual(
		ctx,
		"  Test@Example.COM  ",
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

	if appErr.Status != 409 {
		t.Errorf(
			"expected status 409, got %d",
			appErr.Status,
		)
	}
}

func TestService_RegisterManual_RepositoryError(t *testing.T) {
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
			repositoryErr,
		)

	_, err := test.registrationService.RegisterManual(
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

func TestService_RegisterManual_ExpiredPendingCreatesNewRegistration(t *testing.T) {
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
		CreateManualRegistration(
			ctx,
			gomock.Any(),
		).
		Return(
			registration.CreateManualRegistrationResult{
				VerificationRequest: registrationdb.VerificationRequest{
					ID: verificationID,
				},
				AlreadyPending: false,
			},
			nil,
		)

	result, err := test.registrationService.RegisterManual(
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

	if result.AlreadyPending {
		t.Error("expected AlreadyPending to be false")
	}
}