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

func TestService_FinalizeManualRegistration(t *testing.T) {
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

	continuationToken := "test-continuation-token"
	tokenHash := security.HashToken(continuationToken)

	continuationID := uuid.New()
	pendingRegistrationID := uuid.New()
	userID := uuid.New()

	input := registration.FinalizeManualRegistrationInput{
		ContinuationToken: continuationToken,
		DisplayName:       "John Doe",
		Password:          "password123",
	}

	testStartedAt := time.Now()
	continuationExpiresAt := testStartedAt.Add(15 * time.Minute)
	registrationExpiresAt := testStartedAt.Add(7 * 24 * time.Hour)

	repository.
		EXPECT().
		GetRegistrationContinuation(ctx, tokenHash).
		Return(
			registrationdb.GetRegistrationContinuationRow{
				ID:                    continuationID,
				PendingRegistrationID: pendingRegistrationID,
				Email:                 "john@example.com",
				RegistrationType:      string(registration.RegistrationTypeManual),
				ExpiresAt: pgtype.Timestamptz{
					Time:  continuationExpiresAt,
					Valid: true,
				},
				ConsumedAt: pgtype.Timestamptz{
					Valid: false,
				},
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
		FinalizeManualRegistration(
			ctx,
			gomock.Any(),
		).
		DoAndReturn(func(
			_ context.Context,
			params registration.FinalizeManualRegistrationParams,
		) (registrationdb.CreateUserRow, error) {
			if params.PendingRegistrationID != pendingRegistrationID {
				t.Errorf(
					"expected pending registration ID %s, got %s",
					pendingRegistrationID,
					params.PendingRegistrationID,
				)
			}

			if params.ContinuationID != continuationID {
				t.Errorf(
					"expected continuation ID %s, got %s",
					continuationID,
					params.ContinuationID,
				)
			}

			if params.Email != "john@example.com" {
				t.Errorf(
					"expected email %q, got %q",
					"john@example.com",
					params.Email,
				)
			}

			if params.DisplayName != input.DisplayName {
				t.Errorf(
					"expected display name %q, got %q",
					input.DisplayName,
					params.DisplayName,
				)
			}

			if params.PasswordHash == "" {
				t.Error("expected password hash to be set")
			}

			if params.PasswordHash == input.Password {
				t.Error("expected password to be hashed")
			}

			if !params.EmailVerifiedAt.Valid {
				t.Error("expected email verified at to be valid")
			}

			if !params.EmailVerifiedAt.Time.After(testStartedAt) {
				t.Errorf(
					"expected email verified at after test start, got %v",
					params.EmailVerifiedAt.Time,
				)
			}

			return registrationdb.CreateUserRow{
				ID:          userID,
				Email:       "john@example.com",
				DisplayName: input.DisplayName,
			}, nil
		})

	result, err := service.FinalizeManualRegistration(ctx, input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.UserID != userID {
		t.Errorf(
			"expected user ID %s, got %s",
			userID,
			result.UserID,
		)
	}

	if result.Email != "john@example.com" {
		t.Errorf(
			"expected email %q, got %q",
			"john@example.com",
			result.Email,
		)
	}

	if result.DisplayName != input.DisplayName {
		t.Errorf(
			"expected display name %q, got %q",
			input.DisplayName,
			result.DisplayName,
		)
	}
}

func TestService_FinalizeManualRegistration_GetRegistrationContinuationError(t *testing.T) {
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
	continuationToken := "test-continuation-token"
	tokenHash := security.HashToken(continuationToken)

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

	_, err := service.FinalizeManualRegistration(
		ctx,
		registration.FinalizeManualRegistrationInput{
			ContinuationToken: continuationToken,
			DisplayName:       "John Doe",
			Password:          "password123",
		},
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

func TestService_FinalizeManualRegistration_ValidationError(t *testing.T) {
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
	continuationToken := "test-continuation-token"
	tokenHash := security.HashToken(continuationToken)

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

	_, err := service.FinalizeManualRegistration(
		ctx,
		registration.FinalizeManualRegistrationInput{
			ContinuationToken: continuationToken,
			DisplayName:       "John Doe",
			Password:          "password123",
		},
	)

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

func TestService_FinalizeManualRegistration_InvalidRegistrationType(t *testing.T) {
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
	continuationToken := "test-continuation-token"
	tokenHash := security.HashToken(continuationToken)

	repository.
		EXPECT().
		GetRegistrationContinuation(ctx, tokenHash).
		Return(
			registrationdb.GetRegistrationContinuationRow{
				ID:                    uuid.New(),
				PendingRegistrationID: uuid.New(),
				Email:                 "john@example.com",
				RegistrationType:      "social",
				ExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(15 * time.Minute),
					Valid: true,
				},
				ConsumedAt: pgtype.Timestamptz{
					Valid: false,
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

	_, err := service.FinalizeManualRegistration(
		ctx,
		registration.FinalizeManualRegistrationInput{
			ContinuationToken: continuationToken,
			DisplayName:       "John Doe",
			Password:          "password123",
		},
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	appErr := apperror.FromError(err)

	if appErr.Code != registration.CodeInvalidRegistrationType {
		t.Errorf(
			"expected error code %q, got %q",
			registration.CodeInvalidRegistrationType,
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

func TestService_FinalizeManualRegistration_RepositoryError(t *testing.T) {
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
	continuationToken := "test-continuation-token"
	tokenHash := security.HashToken(continuationToken)

	continuationID := uuid.New()
	pendingRegistrationID := uuid.New()

	repository.
		EXPECT().
		GetRegistrationContinuation(ctx, tokenHash).
		Return(
			registrationdb.GetRegistrationContinuationRow{
				ID:                    continuationID,
				PendingRegistrationID: pendingRegistrationID,
				Email:                 "john@example.com",
				RegistrationType:      string(registration.RegistrationTypeManual),
				ExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(15 * time.Minute),
					Valid: true,
				},
				ConsumedAt: pgtype.Timestamptz{
					Valid: false,
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

	repositoryErr := apperror.Internal(
		errors.New("database connection failed"),
	)

	repository.
		EXPECT().
		FinalizeManualRegistration(
			ctx,
			gomock.Any(),
		).
		Return(
			registrationdb.CreateUserRow{},
			repositoryErr,
		)

	_, err := service.FinalizeManualRegistration(
		ctx,
		registration.FinalizeManualRegistrationInput{
			ContinuationToken: continuationToken,
			DisplayName:       "John Doe",
			Password:          "password123",
		},
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