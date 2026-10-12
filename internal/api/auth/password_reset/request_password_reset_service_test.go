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
	ctx := context.Background()

	t.Run("reuses active reset", func(t *testing.T) {
		test := newTestService(t)

		verificationID := uuid.New()

		test.repository.
			EXPECT().
			UserExists(ctx, "user@example.com").
			Return(true, nil)

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

		if result.UnknownAccount {
			t.Error("expected UnknownAccount to be false")
		}

		// Reusing the open reset must not mail anything or write a second row.
		if len(test.emailSender.Messages) != 0 {
			t.Errorf(
				"expected no email message, got %d",
				len(test.emailSender.Messages),
			)
		}
	})

	t.Run("creates reset", func(t *testing.T) {
		test := newTestService(t)

		verificationID := uuid.New()
		testStartedAt := time.Now()

		test.repository.
			EXPECT().
			UserExists(ctx, "user@example.com").
			Return(true, nil)

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

		if result.UnknownAccount {
			t.Error("expected UnknownAccount to be false")
		}

		// Opening a reset does not mail anything; PIN issuance is a separate
		// step the caller drives.
		if len(test.emailSender.Messages) != 0 {
			t.Errorf(
				"expected no email message, got %d",
				len(test.emailSender.Messages),
			)
		}
	})

	// An address nobody holds is answered, not refused, and the answer is a
	// placeholder that resolves to nothing. No repository call beyond the
	// existence check is expected: gomock treats reaching any other method as a
	// failure, which is what makes "creates no state" an assertion here rather
	// than an inference.
	t.Run("unknown account gets an unpersisted placeholder", func(t *testing.T) {
		test := newTestService(t)

		test.repository.
			EXPECT().
			UserExists(ctx, "nobody@example.com").
			Return(false, nil)

		result, err := test.passwordResetService.RequestPasswordReset(
			ctx,
			"nobody@example.com",
		)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if result.VerificationID == uuid.Nil {
			t.Error("expected a placeholder verification ID")
		}

		if !result.UnknownAccount {
			t.Error("expected UnknownAccount to be true")
		}

		if len(test.emailSender.Messages) != 0 {
			t.Errorf(
				"expected no email message, got %d",
				len(test.emailSender.Messages),
			)
		}
	})

	t.Run("user exists repository error", func(t *testing.T) {
		test := newTestService(t)

		repositoryErr := apperror.Internal(
			errors.New("database connection failed"),
		)

		test.repository.
			EXPECT().
			UserExists(ctx, "user@example.com").
			Return(false, repositoryErr)

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
		test := newTestService(t)

		repositoryErr := apperror.Internal(
			errors.New("database connection failed"),
		)

		test.repository.
			EXPECT().
			UserExists(ctx, "user@example.com").
			Return(true, nil)

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
		test := newTestService(t)

		repositoryErr := apperror.Internal(
			errors.New("database connection failed"),
		)

		test.repository.
			EXPECT().
			UserExists(ctx, "user@example.com").
			Return(true, nil)

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

// TestService_RequestPasswordReset_PlaceholderIsUnusable pins the property the
// placeholder depends on: it is an ordinary unknown verification id to every
// endpoint downstream, so the fact that this service handed it out grants
// nothing.
//
// Every repository expectation here is for a lookup that returns nothing. The
// absence of any write expectation is the assertion that a placeholder cannot
// be advanced into real state.
func TestService_RequestPasswordReset_PlaceholderIsUnusable(t *testing.T) {
	ctx := context.Background()

	test := newTestService(t)

	test.repository.
		EXPECT().
		UserExists(ctx, "nobody@example.com").
		Return(false, nil)

	result, err := test.passwordResetService.RequestPasswordReset(
		ctx,
		"nobody@example.com",
	)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	dummyID := result.VerificationID

	// Every endpoint below resolves a verification id to a row. There is no
	// row, so each one refuses through its ordinary not-found path.
	notFound := apperror.NotFound(errors.New("verification not found"))

	test.repository.
		EXPECT().
		GetVerification(ctx, dummyID).
		Return(passwordresetdb.GetVerificationRow{}, notFound)

	if _, err := test.passwordResetService.GetVerification(
		ctx,
		dummyID,
	); !errors.Is(err, notFound) {
		t.Errorf("GetVerification: expected not found, got %v", err)
	}

	test.repository.
		EXPECT().
		GetVerification(ctx, dummyID).
		Return(passwordresetdb.GetVerificationRow{}, notFound)

	if _, err := test.passwordResetService.CreatePIN(
		ctx,
		dummyID,
	); !errors.Is(err, notFound) {
		t.Errorf("CreatePIN: expected not found, got %v", err)
	}

	test.repository.
		EXPECT().
		GetVerification(ctx, dummyID).
		Return(passwordresetdb.GetVerificationRow{}, notFound)

	if _, err := test.passwordResetService.ResendVerification(
		ctx,
		dummyID,
	); !errors.Is(err, notFound) {
		t.Errorf("ResendVerification: expected not found, got %v", err)
	}

	test.repository.
		EXPECT().
		GetVerification(ctx, dummyID).
		Return(passwordresetdb.GetVerificationRow{}, notFound)

	if _, err := test.passwordResetService.VerifyPasswordReset(
		ctx,
		dummyID,
		"123456",
	); !errors.Is(err, notFound) {
		t.Errorf("VerifyPasswordReset: expected not found, got %v", err)
	}

	// Not one of those reached PIN generation, a code row, or a message. The
	// mock would have failed on an unexpected call, and the sender is shared
	// across all of them.
	if len(test.emailSender.Messages) != 0 {
		t.Errorf(
			"a placeholder verification ID sent %d message(s)",
			len(test.emailSender.Messages),
		)
	}
}

// TestService_RequestPasswordReset_PlaceholderIsNotDeterministic checks that two
// requests for the same unknown address do not hand back the same value.
//
// A stable placeholder would let a caller tell an address it has already asked
// about from one it has not, which is itself a (much weaker) existence signal,
// and would make the response a function of the request rather than of the
// account.
func TestService_RequestPasswordReset_PlaceholderIsNotDeterministic(t *testing.T) {
	ctx := context.Background()

	seen := make(map[uuid.UUID]struct{})

	for i := 0; i < 5; i++ {
		test := newTestService(t)

		test.repository.
			EXPECT().
			UserExists(ctx, "nobody@example.com").
			Return(false, nil)

		result, err := test.passwordResetService.RequestPasswordReset(
			ctx,
			"nobody@example.com",
		)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if _, seenAgain := seen[result.VerificationID]; seenAgain {
			t.Fatalf(
				"placeholder %s was returned twice",
				result.VerificationID,
			)
		}

		seen[result.VerificationID] = struct{}{}

		// The placeholder must not be the zero UUID either: that is a value a
		// client could mistake for "unset" and branch on.
		if result.VerificationID == uuid.Nil {
			t.Fatal("expected a non-nil placeholder")
		}
	}
}
