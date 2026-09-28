package password_reset_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func TestService_GetPasswordResetContinuation(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		test := newTestService(t)

		token := "test-continuation-token"
		tokenHash := security.HashToken(token)

		row := pendingContinuation(
			"user@example.com",
		)

		row.HasPasswordCredential = true

		test.repository.
			EXPECT().
			GetPasswordResetContinuation(ctx, tokenHash).
			Return(row, nil)

		result, err := test.passwordResetService.GetPasswordResetContinuation(
			ctx,
			token,
		)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if result.Email != row.Email {
			t.Errorf(
				"expected email %q, got %q",
				row.Email,
				result.Email,
			)
		}

		if result.HasPasswordCredential != row.HasPasswordCredential {
			t.Errorf(
				"expected has password credential %v, got %v",
				row.HasPasswordCredential,
				result.HasPasswordCredential,
			)
		}
	})

	t.Run("consumed", func(t *testing.T) {
		test := newTestService(t)

		token := "test-continuation-token"

		row := pendingContinuation(
			"user@example.com",
		)

		row.ConsumedAt = pgtype.Timestamptz{
			Time:  time.Now(),
			Valid: true,
		}

		test.repository.
			EXPECT().
			GetPasswordResetContinuation(
				ctx,
				security.HashToken(token),
			).
			Return(row, nil)

		_, err := test.passwordResetService.GetPasswordResetContinuation(
			ctx,
			token,
		)

		if err == nil {
			t.Fatal("expected error, got nil")
		}

		appErr := apperror.FromError(err)

		if appErr.Code != passwordreset.CodePasswordResetContinuationConsumed {
			t.Errorf(
				"expected error code %q, got %q",
				passwordreset.CodePasswordResetContinuationConsumed,
				appErr.Code,
			)
		}
	})

	t.Run("expired", func(t *testing.T) {
		test := newTestService(t)

		token := "test-continuation-token"

		row := pendingContinuation(
			"user@example.com",
		)

		row.ExpiresAt = pgtype.Timestamptz{
			Time:  time.Now().Add(-time.Minute),
			Valid: true,
		}

		test.repository.
			EXPECT().
			GetPasswordResetContinuation(
				ctx,
				security.HashToken(token),
			).
			Return(row, nil)

		_, err := test.passwordResetService.GetPasswordResetContinuation(
			ctx,
			token,
		)

		if err == nil {
			t.Fatal("expected error, got nil")
		}

		appErr := apperror.FromError(err)

		if appErr.Code != passwordreset.CodePasswordResetContinuationExpired {
			t.Errorf(
				"expected error code %q, got %q",
				passwordreset.CodePasswordResetContinuationExpired,
				appErr.Code,
			)
		}
	})

	t.Run("reset not pending", func(t *testing.T) {
		test := newTestService(t)

		token := "test-continuation-token"

		row := pendingContinuation(
			"user@example.com",
		)

		row.PasswordResetStatus = string(
			passwordreset.PendingPasswordResetCompleted,
		)

		test.repository.
			EXPECT().
			GetPasswordResetContinuation(
				ctx,
				security.HashToken(token),
			).
			Return(row, nil)

		_, err := test.passwordResetService.GetPasswordResetContinuation(
			ctx,
			token,
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
	})

	t.Run("reset expired", func(t *testing.T) {
		test := newTestService(t)

		token := "test-continuation-token"

		row := pendingContinuation(
			"user@example.com",
		)

		row.PasswordResetExpiresAt = pgtype.Timestamptz{
			Time:  time.Now().Add(-time.Minute),
			Valid: true,
		}

		test.repository.
			EXPECT().
			GetPasswordResetContinuation(
				ctx,
				security.HashToken(token),
			).
			Return(row, nil)

		_, err := test.passwordResetService.GetPasswordResetContinuation(
			ctx,
			token,
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
	})

	t.Run("repository error", func(t *testing.T) {
		test := newTestService(t)

		token := "test-continuation-token"
		repositoryErr := errors.New("lookup")

		test.repository.
			EXPECT().
			GetPasswordResetContinuation(
				ctx,
				security.HashToken(token),
			).
			Return(
				passwordresetdb.GetPasswordResetContinuationRow{},
				repositoryErr,
			)

		_, err := test.passwordResetService.GetPasswordResetContinuation(
			ctx,
			token,
		)

		if !errors.Is(err, repositoryErr) {
			t.Fatalf(
				"expected repository error, got %v",
				err,
			)
		}
	})
}