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

func TestService_FinalizePasswordReset(t *testing.T) {
	ctx := context.Background()
	t.Run("success hashes password and passes reset identifiers", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		continuationID, resetID, userID := uuid.New(), uuid.New(), uuid.New()
		token, password := "token", "new-password"
		repository.EXPECT().GetPasswordResetContinuation(ctx, security.HashToken(token)).Return(pendingContinuation(continuationID, resetID, "user@example.com"), nil)
		repository.EXPECT().GetUserByEmail(ctx, "user@example.com").Return(passwordresetdb.GetUserByEmailRow{ID: userID}, nil)
		repository.EXPECT().UpsertPasswordCredentialAndCompleteReset(ctx, gomock.Any()).DoAndReturn(func(_ context.Context, params passwordreset.UpsertPasswordCredentialAndCompleteResetParams) error {
			if params.UserID != userID || params.PasswordResetContinuationID != continuationID || params.PendingPasswordResetID != resetID || params.PasswordHash == "" || params.PasswordHash == password {
				t.Errorf("unexpected %#v", params)
			}
			verified, err := security.NewPasswordHasher().Verify(password, params.PasswordHash)
			if err != nil || !verified.Match {
				t.Error("password was not hashed correctly")
			}
			return nil
		})
		if err := newService(repository).FinalizePasswordReset(ctx, passwordreset.FinalizePasswordResetInput{ContinuationToken: token, Password: password}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("continuation validation fails before user lookup", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		token := "used"
		row := pendingContinuation(uuid.New(), uuid.New(), "user@example.com")
		row.ConsumedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
		repository.EXPECT().GetPasswordResetContinuation(ctx, security.HashToken(token)).Return(row, nil)
		err := newService(repository).FinalizePasswordReset(ctx, passwordreset.FinalizePasswordResetInput{ContinuationToken: token, Password: "new-password"})
		if got := apperror.FromError(err).Code; got != passwordreset.CodePasswordResetContinuationConsumed {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("user lookup error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		token, want := "token", errors.New("user")
		repository.EXPECT().GetPasswordResetContinuation(ctx, security.HashToken(token)).Return(pendingContinuation(uuid.New(), uuid.New(), "user@example.com"), nil)
		repository.EXPECT().GetUserByEmail(ctx, "user@example.com").Return(passwordresetdb.GetUserByEmailRow{}, want)
		err := newService(repository).FinalizePasswordReset(ctx, passwordreset.FinalizePasswordResetInput{ContinuationToken: token, Password: "new-password"})
		if !errors.Is(err, want) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("upsert error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		token, want := "token", errors.New("upsert")
		repository.EXPECT().GetPasswordResetContinuation(ctx, security.HashToken(token)).Return(pendingContinuation(uuid.New(), uuid.New(), "user@example.com"), nil)
		repository.EXPECT().GetUserByEmail(ctx, "user@example.com").Return(passwordresetdb.GetUserByEmailRow{ID: uuid.New()}, nil)
		repository.EXPECT().UpsertPasswordCredentialAndCompleteReset(ctx, gomock.Any()).Return(want)
		err := newService(repository).FinalizePasswordReset(ctx, passwordreset.FinalizePasswordResetInput{ContinuationToken: token, Password: "new-password"})
		if !errors.Is(err, want) {
			t.Fatalf("got %v", err)
		}
	})
}
