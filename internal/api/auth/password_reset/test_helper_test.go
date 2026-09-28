package password_reset_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/mocks"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"go.uber.org/mock/gomock"
)

type testService struct {
	passwordResetService *passwordreset.Service
	repository           *mocks.MockRepository
}

func newTestService(t *testing.T) *testService {
	t.Helper()

	ctrl := gomock.NewController(t)
	repository := mocks.NewMockRepository(ctrl)

	return &testService{
		passwordResetService: passwordreset.NewService(
			repository,
			security.NewPasswordHasher(),
			security.NewVerificationCodeHasher([]byte("test-secret")),
		),
		repository: repository,
	}
}

func pendingContinuation(
	email string,
) passwordresetdb.GetPasswordResetContinuationRow {
	now := time.Now()

	return passwordresetdb.GetPasswordResetContinuationRow{
		Email: email,
		ExpiresAt: pgtype.Timestamptz{
			Time:  now.Add(15 * time.Minute),
			Valid: true,
		},
		ConsumedAt: pgtype.Timestamptz{
			Valid: false,
		},
		PasswordResetStatus: string(
			passwordreset.PendingPasswordResetPending,
		),
		PasswordResetExpiresAt: pgtype.Timestamptz{
			Time:  now.Add(7 * 24 * time.Hour),
			Valid: true,
		},
	}
}

func pendingVerification(
	verificationID uuid.UUID,
	pendingPasswordResetID uuid.UUID,
) passwordresetdb.GetVerificationRow {
	return passwordresetdb.GetVerificationRow{
		ID:        verificationID,
		SubjectID: pendingPasswordResetID,
		Status:    string(passwordreset.VerificationRequestPending),
		PasswordResetStatus: string(
			passwordreset.PendingPasswordResetPending,
		),
		PasswordResetExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(7 * 24 * time.Hour),
			Valid: true,
		},
	}
}