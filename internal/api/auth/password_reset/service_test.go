package password_reset_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"

	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func newService(repository *mocks.MockRepository) *passwordreset.Service {
	return passwordreset.NewService(repository, security.NewPasswordHasher(), security.NewVerificationCodeHasher([]byte("test-secret")))
}

func pendingVerification(id, resetID uuid.UUID) passwordresetdb.GetVerificationRow {
	return passwordresetdb.GetVerificationRow{ID: id, SubjectID: resetID, Status: string(passwordreset.VerificationRequestPending), PasswordResetStatus: string(passwordreset.PendingPasswordResetPending), PasswordResetExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}
}

func pendingContinuation(id, resetID uuid.UUID, email string) passwordresetdb.GetPasswordResetContinuationRow {
	return passwordresetdb.GetPasswordResetContinuationRow{ID: id, PendingPasswordResetID: resetID, Email: email, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, PasswordResetStatus: string(passwordreset.PendingPasswordResetPending), PasswordResetExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}
}

func TestService_RequestPasswordReset(t *testing.T) {
	ctx := context.Background()
	t.Run("reuses active reset", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		verificationID := uuid.New()
		repository.EXPECT().GetUserByEmail(ctx, "user@example.com").Return(passwordresetdb.GetUserByEmailRow{}, nil)
		repository.EXPECT().GetActivePasswordResetByEmail(ctx, "user@example.com").Return(passwordresetdb.GetActivePasswordResetByEmailRow{VerificationID: verificationID}, true, nil)
		result, err := newService(repository).RequestPasswordReset(ctx, " User@Example.com ")
		if err != nil || result.VerificationID != verificationID || !result.AlreadyPending {
			t.Fatalf("unexpected result %#v, err %v", result, err)
		}
	})
	t.Run("creates reset", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		verificationID := uuid.New()
		repository.EXPECT().GetUserByEmail(ctx, "user@example.com").Return(passwordresetdb.GetUserByEmailRow{}, nil)
		repository.EXPECT().GetActivePasswordResetByEmail(ctx, "user@example.com").Return(passwordresetdb.GetActivePasswordResetByEmailRow{}, false, nil)
		repository.EXPECT().CreatePasswordReset(ctx, gomock.Any()).DoAndReturn(func(_ context.Context, params passwordreset.CreatePasswordResetParams) (passwordresetdb.VerificationRequest, error) {
			if params.Email != "user@example.com" || !params.ResetExpiresAt.Valid || !params.CodeExpiresAt.Valid || params.CodeHash == "" {
				t.Errorf("unexpected params: %#v", params)
			}
			return passwordresetdb.VerificationRequest{ID: verificationID}, nil
		})
		result, err := newService(repository).RequestPasswordReset(ctx, " User@Example.com ")
		if err != nil || result.VerificationID != verificationID || result.AlreadyPending {
			t.Fatalf("unexpected result %#v, err %v", result, err)
		}
	})
	for _, stage := range []string{"user", "active", "create"} {
		t.Run("propagates "+stage+" repository error", func(t *testing.T) {
			ctrl := gomock.NewController(t)
			repository := mocks.NewMockRepository(ctrl)
			want := apperror.Internal(errors.New(stage))
			if stage == "user" {
				repository.EXPECT().GetUserByEmail(ctx, "user@example.com").Return(passwordresetdb.GetUserByEmailRow{}, want)
			} else {
				repository.EXPECT().GetUserByEmail(ctx, "user@example.com").Return(passwordresetdb.GetUserByEmailRow{}, nil)
				if stage == "active" {
					repository.EXPECT().GetActivePasswordResetByEmail(ctx, "user@example.com").Return(passwordresetdb.GetActivePasswordResetByEmailRow{}, false, want)
				} else {
					repository.EXPECT().GetActivePasswordResetByEmail(ctx, "user@example.com").Return(passwordresetdb.GetActivePasswordResetByEmailRow{}, false, nil)
					repository.EXPECT().CreatePasswordReset(ctx, gomock.Any()).Return(passwordresetdb.VerificationRequest{}, want)
				}
			}
			_, err := newService(repository).RequestPasswordReset(ctx, "user@example.com")
			if !errors.Is(err, want) {
				t.Fatalf("expected %v, got %v", want, err)
			}
		})
	}
}
