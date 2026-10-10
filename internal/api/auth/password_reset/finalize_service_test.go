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
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
)

func TestService_FinalizePasswordReset(t *testing.T) {
	ctx := context.Background()

	t.Run("success hashes password and passes reset identifiers", func(t *testing.T) {
		test := newTestService(t)

		continuationID := uuid.New()
		resetID := uuid.New()
		userID := uuid.New()

		token := "token"
		password := "new-password"

		row := pendingContinuation("user@example.com")
		row.ID = continuationID
		row.PendingPasswordResetID = resetID

		test.repository.
			EXPECT().
			GetPasswordResetContinuation(
				ctx,
				security.HashToken(token),
			).
			Return(row, nil)

		test.repository.
			EXPECT().
			GetUserByEmail(
				ctx,
				"user@example.com",
			).
			Return(
				passwordresetdb.GetUserByEmailRow{
					ID: userID,
				},
				nil,
			)

		test.repository.
			EXPECT().
			UpsertPasswordCredentialAndCompleteReset(
				ctx,
				gomock.Any(),
			).
			DoAndReturn(func(
				_ context.Context,
				params passwordreset.UpsertPasswordCredentialAndCompleteResetParams,
			) error {
				if params.UserID != userID ||
					params.PasswordResetContinuationID != continuationID ||
					params.PendingPasswordResetID != resetID ||
					params.PasswordHash == "" ||
					params.PasswordHash == password {
					t.Errorf("unexpected %#v", params)
				}

				verified, err := testutil.NewPasswordHasher().Verify(context.Background(),
					password,
					params.PasswordHash,
				)
				if err != nil || !verified.Match {
					t.Error("password was not hashed correctly")
				}

				return nil
			})

		err := test.passwordResetService.FinalizePasswordReset(
			ctx,
			passwordreset.FinalizePasswordResetInput{
				ContinuationToken: token,
				Password:          password,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("continuation validation fails before user lookup", func(t *testing.T) {
		test := newTestService(t)

		token := "used"

		row := pendingContinuation("user@example.com")
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

		err := test.passwordResetService.FinalizePasswordReset(
			ctx,
			passwordreset.FinalizePasswordResetInput{
				ContinuationToken: token,
				Password:          "new-password",
			},
		)

		if got := apperror.FromError(err).Code; got != passwordreset.CodePasswordResetContinuationConsumed {
			t.Fatalf("got %s", got)
		}
	})

	t.Run("user lookup error", func(t *testing.T) {
		test := newTestService(t)

		token := "token"
		want := errors.New("user")

		row := pendingContinuation("user@example.com")

		test.repository.
			EXPECT().
			GetPasswordResetContinuation(
				ctx,
				security.HashToken(token),
			).
			Return(row, nil)

		test.repository.
			EXPECT().
			GetUserByEmail(
				ctx,
				"user@example.com",
			).
			Return(
				passwordresetdb.GetUserByEmailRow{},
				want,
			)

		err := test.passwordResetService.FinalizePasswordReset(
			ctx,
			passwordreset.FinalizePasswordResetInput{
				ContinuationToken: token,
				Password:          "new-password",
			},
		)

		if !errors.Is(err, want) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("upsert error", func(t *testing.T) {
		test := newTestService(t)

		token := "token"
		want := errors.New("upsert")

		row := pendingContinuation("user@example.com")

		test.repository.
			EXPECT().
			GetPasswordResetContinuation(
				ctx,
				security.HashToken(token),
			).
			Return(row, nil)

		test.repository.
			EXPECT().
			GetUserByEmail(
				ctx,
				"user@example.com",
			).
			Return(
				passwordresetdb.GetUserByEmailRow{
					ID: uuid.New(),
				},
				nil,
			)

		test.repository.
			EXPECT().
			UpsertPasswordCredentialAndCompleteReset(
				ctx,
				gomock.Any(),
			).
			Return(want)

		err := test.passwordResetService.FinalizePasswordReset(
			ctx,
			passwordreset.FinalizePasswordResetInput{
				ContinuationToken: token,
				Password:          "new-password",
			},
		)

		if !errors.Is(err, want) {
			t.Fatalf("got %v", err)
		}
	})
}
