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
)

func TestService_ResendVerification(t *testing.T) {
	ctx := context.Background()
	t.Run("rejects cooldown", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		id := uuid.New()
		row := pendingVerification(id, uuid.New())
		row.LastSentAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
		repository.EXPECT().GetVerification(ctx, id).Return(row, nil)
		_, err := newService(repository).ResendVerification(ctx, id)
		if got := apperror.FromError(err).Code; got != passwordreset.CodeVerificationResendCooldown {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("get verification error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		id, want := uuid.New(), errors.New("lookup")
		repository.EXPECT().GetVerification(ctx, id).Return(passwordresetdb.GetVerificationRow{}, want)
		_, err := newService(repository).ResendVerification(ctx, id)
		if !errors.Is(err, want) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("resend error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		id, want := uuid.New(), errors.New("resend")
		repository.EXPECT().GetVerification(ctx, id).Return(pendingVerification(id, uuid.New()), nil)
		repository.EXPECT().ResendVerification(ctx, gomock.Any()).Return(passwordresetdb.VerificationRequest{}, want)
		_, err := newService(repository).ResendVerification(ctx, id)
		if !errors.Is(err, want) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockRepository(ctrl)
		id := uuid.New()
		repository.EXPECT().GetVerification(ctx, id).Return(pendingVerification(id, uuid.New()), nil)
		repository.EXPECT().ResendVerification(ctx, gomock.Any()).DoAndReturn(func(_ context.Context, params passwordreset.ResendVerificationParams) (passwordresetdb.VerificationRequest, error) {
			if params.VerificationID != id || !params.CodeExpiresAt.Valid || params.CodeHash == "" {
				t.Errorf("unexpected %#v", params)
			}
			return passwordresetdb.VerificationRequest{}, nil
		})
		result, err := newService(repository).ResendVerification(ctx, id)
		if err != nil || result.VerificationID != id {
			t.Fatalf("unexpected %#v %v", result, err)
		}
	})
}
