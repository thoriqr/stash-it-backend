package password_reset_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"go.uber.org/mock/gomock"
)

func TestService_RequestPasswordReset(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()

	t.Run("reuses active reset", func(t *testing.T) {
		verificationID := uuid.New()

		test.repository.
			EXPECT().
			GetUserByEmail(
				ctx,
				"user@example.com",
			).
			Return(
				passwordresetdb.GetUserByEmailRow{},
				nil,
			)

		test.repository.
			EXPECT().
			GetActivePasswordResetByEmail(
				ctx,
				"user@example.com",
			).
			Return(
				passwordresetdb.GetActivePasswordResetByEmailRow{
					VerificationID: verificationID,
				},
				true,
				nil,
			)

		result, err := test.passwordResetService.RequestPasswordReset(
			ctx,
			" User@Example.com ",
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
	})

	t.Run("creates reset", func(t *testing.T) {
		verificationID := uuid.New()
		testStartedAt := time.Now()

		test.repository.
			EXPECT().
			GetUserByEmail(
				ctx,
				"user@example.com",
			).
			Return(
				passwordresetdb.GetUserByEmailRow{},
				nil,
			)

		test.repository.
			EXPECT().
			GetActivePasswordResetByEmail(
				ctx,
				"user@example.com",
			).
			Return(
				passwordresetdb.GetActivePasswordResetByEmailRow{},
				false,
				nil,
			)

		test.repository.
			EXPECT().
			CreatePasswordReset(
				ctx,
				gomock.Any(),
			).
			DoAndReturn(func(
				_ context.Context,
				params passwordreset.CreatePasswordResetParams,
			) (passwordresetdb.VerificationRequest, error) {
				if params.Email != "user@example.com" {
					t.Errorf(
						"expected normalized email %q, got %q",
						"user@example.com",
						params.Email,
					)
				}

				if !params.ResetExpiresAt.Valid {
					t.Error("expected reset expiration to be valid")
				}

				now := time.Now()

				resetMin := testStartedAt.Add(15 * time.Minute)
				resetMax := now.Add(15 * time.Minute)

				if params.ResetExpiresAt.Time.Before(resetMin) ||
					params.ResetExpiresAt.Time.After(resetMax) {
					t.Errorf(
						"unexpected reset expiration: %v",
						params.ResetExpiresAt.Time,
					)
				}

				return passwordresetdb.VerificationRequest{
					ID: verificationID,
				}, nil
			})

		result, err := test.passwordResetService.RequestPasswordReset(
			ctx,
			" User@Example.com ",
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
	})

	t.Run("user repository error", func(t *testing.T) {
		repositoryErr := apperror.Internal(
			errors.New("database connection failed"),
		)

		test.repository.
			EXPECT().
			GetUserByEmail(
				ctx,
				"user@example.com",
			).
			Return(
				passwordresetdb.GetUserByEmailRow{},
				repositoryErr,
			)

		_, err := test.passwordResetService.RequestPasswordReset(
			ctx,
			"user@example.com",
		)

		if !errors.Is(err, repositoryErr) {
			t.Fatalf(
				"expected repository error, got %v",
				err,
			)
		}
	})

	t.Run("active reset repository error", func(t *testing.T) {
		repositoryErr := apperror.Internal(
			errors.New("database connection failed"),
		)

		test.repository.
			EXPECT().
			GetUserByEmail(
				ctx,
				"user@example.com",
			).
			Return(
				passwordresetdb.GetUserByEmailRow{},
				nil,
			)

		test.repository.
			EXPECT().
			GetActivePasswordResetByEmail(
				ctx,
				"user@example.com",
			).
			Return(
				passwordresetdb.GetActivePasswordResetByEmailRow{},
				false,
				repositoryErr,
			)

		_, err := test.passwordResetService.RequestPasswordReset(
			ctx,
			"user@example.com",
		)

		if !errors.Is(err, repositoryErr) {
			t.Fatalf(
				"expected repository error, got %v",
				err,
			)
		}
	})

	t.Run("create reset repository error", func(t *testing.T) {
		repositoryErr := apperror.Internal(
			errors.New("database connection failed"),
		)

		test.repository.
			EXPECT().
			GetUserByEmail(
				ctx,
				"user@example.com",
			).
			Return(
				passwordresetdb.GetUserByEmailRow{},
				nil,
			)

		test.repository.
			EXPECT().
			GetActivePasswordResetByEmail(
				ctx,
				"user@example.com",
			).
			Return(
				passwordresetdb.GetActivePasswordResetByEmailRow{},
				false,
				nil,
			)

		test.repository.
			EXPECT().
			CreatePasswordReset(
				ctx,
				gomock.Any(),
			).
			Return(
				passwordresetdb.VerificationRequest{},
				repositoryErr,
			)

		_, err := test.passwordResetService.RequestPasswordReset(
			ctx,
			"user@example.com",
		)

		if !errors.Is(err, repositoryErr) {
			t.Fatalf(
				"expected repository error, got %v",
				err,
			)
		}
	})
}