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

func TestService_GetPasswordResetContinuation(t *testing.T) {
	ctx := context.Background()
	for _, credential := range []bool{false, true} {
		t.Run("success", func(t *testing.T) {
			ctrl := gomock.NewController(t)
			repository := mocks.NewMockRepository(ctrl)
			token := "token"
			row := pendingContinuation(uuid.New(), uuid.New(), "user@example.com")
			row.HasPasswordCredential = credential
			repository.EXPECT().GetPasswordResetContinuation(ctx, security.HashToken(token)).Return(row, nil)
			result, err := newService(repository).GetPasswordResetContinuation(ctx, token)
			if err != nil || result.Email != row.Email || result.HasPasswordCredential != credential {
				t.Fatalf("unexpected %#v %v", result, err)
			}
		})
	}
	tests := []struct {
		name string
		row  passwordresetdb.GetPasswordResetContinuationRow
		code string
	}{{"consumed", func() passwordresetdb.GetPasswordResetContinuationRow {
		row := pendingContinuation(uuid.New(), uuid.New(), "x")
		row.ConsumedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
		return row
	}(), passwordreset.CodePasswordResetContinuationConsumed}, {"expired", func() passwordresetdb.GetPasswordResetContinuationRow {
		row := pendingContinuation(uuid.New(), uuid.New(), "x")
		row.ExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
		return row
	}(), passwordreset.CodePasswordResetContinuationExpired}, {"reset not pending", func() passwordresetdb.GetPasswordResetContinuationRow {
		row := pendingContinuation(uuid.New(), uuid.New(), "x")
		row.PasswordResetStatus = string(passwordreset.PendingPasswordResetCompleted)
		return row
	}(), passwordreset.CodePasswordResetNotPending}, {"reset expired", func() passwordresetdb.GetPasswordResetContinuationRow {
		row := pendingContinuation(uuid.New(), uuid.New(), "x")
		row.PasswordResetExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
		return row
	}(), passwordreset.CodePasswordResetExpired}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			repository := mocks.NewMockRepository(ctrl)
			token := uuid.NewString()
			repository.EXPECT().GetPasswordResetContinuation(ctx, security.HashToken(token)).Return(tt.row, nil)
			_, err := newService(repository).GetPasswordResetContinuation(ctx, token)
			if got := apperror.FromError(err).Code; got != tt.code {
				t.Fatalf("got %s", got)
			}
		})
	}
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)
	token, want := "bad", errors.New("lookup")
	repository.EXPECT().GetPasswordResetContinuation(ctx, security.HashToken(token)).Return(passwordresetdb.GetPasswordResetContinuationRow{}, want)
	_, err := newService(repository).GetPasswordResetContinuation(ctx, token)
	if !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}
