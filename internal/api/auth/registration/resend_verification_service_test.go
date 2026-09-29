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
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func TestService_ResendVerification(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		test := newTestService(t)

		verificationID := uuid.New()
		testEmail := "test@example.com"
		startedAt := time.Now()

		test.repository.
			EXPECT().
			GetVerification(ctx, verificationID).
			Return(
				registrationdb.GetVerificationRow{
					ID:                 verificationID,
					Email:              testEmail,
					Status:             string(registration.VerificationRequestPending),
					RegistrationStatus: string(registration.PendingRegistrationPending),
					RegistrationExpiresAt: pgtype.Timestamptz{
						Time:  startedAt.Add(7 * 24 * time.Hour),
						Valid: true,
					},
					LastSentAt: pgtype.Timestamptz{
						Time:  startedAt.Add(-2 * time.Minute),
						Valid: true,
					},
				},
				nil,
			)

		test.repository.
			EXPECT().
			IssueVerificationCode(
				ctx,
				gomock.Any(),
			).
			DoAndReturn(func(
				_ context.Context,
				params registration.IssueVerificationCodeParams,
			) (registrationdb.VerificationRequest, error) {
				if params.VerificationID != verificationID {
					t.Errorf(
						"expected verification ID %s, got %s",
						verificationID,
						params.VerificationID,
					)
				}

				if params.CodeHash == "" {
					t.Error("expected code hash to be set")
				}

				if !params.CodeExpiresAt.Valid {
					t.Error("expected code expiration to be valid")
				}

				if params.CodeExpiresAt.Time.Before(
					startedAt.Add(5 * time.Minute),
				) {
					t.Errorf(
						"expected code expiration around 5 minutes, got %v",
						params.CodeExpiresAt.Time,
					)
				}

				return registrationdb.VerificationRequest{
					ID: verificationID,
				}, nil
			})

		result, err := test.registrationService.ResendVerification(
			ctx,
			verificationID,
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

		if len(test.emailSender.Messages) != 1 {
			t.Fatalf(
				"expected 1 email message, got %d",
				len(test.emailSender.Messages),
			)
		}

		message := test.emailSender.Messages[0]

		if message.To.Email != testEmail {
			t.Errorf(
				"expected recipient %q, got %q",
				testEmail,
				message.To.Email,
			)
		}
	})

	t.Run("rejects cooldown", func(t *testing.T) {
		test := newTestService(t)

		verificationID := uuid.New()

		test.repository.
			EXPECT().
			GetVerification(ctx, verificationID).
			Return(
				registrationdb.GetVerificationRow{
					ID:   verificationID,
					Status: string(registration.VerificationRequestPending),
					RegistrationStatus: string(
						registration.PendingRegistrationPending,
					),
					RegistrationExpiresAt: pgtype.Timestamptz{
						Time:  time.Now().Add(7 * 24 * time.Hour),
						Valid: true,
					},
					LastSentAt: pgtype.Timestamptz{
						Time:  time.Now().Add(-30 * time.Second),
						Valid: true,
					},
				},
				nil,
			)

		_, err := test.registrationService.ResendVerification(
			ctx,
			verificationID,
		)

		if err == nil {
			t.Fatal("expected error, got nil")
		}

		appErr := apperror.FromError(err)

		if appErr.Code != registration.CodeVerificationResendCooldown {
			t.Errorf(
				"expected error code %q, got %q",
				registration.CodeVerificationResendCooldown,
				appErr.Code,
			)
		}
	})

	t.Run("get verification error", func(t *testing.T) {
		test := newTestService(t)

		verificationID := uuid.New()
		repositoryErr := errors.New("lookup")

		test.repository.
			EXPECT().
			GetVerification(ctx, verificationID).
			Return(
				registrationdb.GetVerificationRow{},
				repositoryErr,
			)

		_, err := test.registrationService.ResendVerification(
			ctx,
			verificationID,
		)

		if !errors.Is(err, repositoryErr) {
			t.Fatalf(
				"expected repository error, got %v",
				err,
			)
		}
	})

	t.Run("issue verification code error", func(t *testing.T) {
		test := newTestService(t)

		verificationID := uuid.New()
		repositoryErr := errors.New("issue verification code")

		test.repository.
			EXPECT().
			GetVerification(ctx, verificationID).
			Return(
				registrationdb.GetVerificationRow{
					ID:   verificationID,
					Status: string(registration.VerificationRequestPending),
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

		test.repository.
			EXPECT().
			IssueVerificationCode(
				ctx,
				gomock.Any(),
			).
			Return(
				registrationdb.VerificationRequest{},
				repositoryErr,
			)

		_, err := test.registrationService.ResendVerification(
			ctx,
			verificationID,
		)

		if !errors.Is(err, repositoryErr) {
			t.Fatalf(
				"expected repository error, got %v",
				err,
			)
		}
	})
}

func TestService_ResendVerification_EmailSenderError(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()
	testEmail := "test@example.com"
	startedAt := time.Now()

	test.repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{
				ID:                 verificationID,
				Email:              testEmail,
				Status:             string(registration.VerificationRequestPending),
				RegistrationStatus: string(
					registration.PendingRegistrationPending,
				),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  startedAt.Add(7 * 24 * time.Hour),
					Valid: true,
				},
				LastSentAt: pgtype.Timestamptz{
					Time:  startedAt.Add(-2 * time.Minute),
					Valid: true,
				},
			},
			nil,
		)

	test.repository.
		EXPECT().
		IssueVerificationCode(
			ctx,
			gomock.Any(),
		).
		Return(
			registrationdb.VerificationRequest{
				ID: verificationID,
			},
			nil,
		)

	emailErr := errors.New("email provider unavailable")
	test.emailSender.Err = emailErr

	_, err := test.registrationService.ResendVerification(
		ctx,
		verificationID,
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, emailErr) {
		t.Fatalf(
			"expected email sender error, got %v",
			err,
		)
	}

	appErr := apperror.FromError(err)

	if appErr.Code != apperror.CodeInternal {
		t.Errorf(
			"expected error code %q, got %q",
			apperror.CodeInternal,
			appErr.Code,
		)
	}
}

func TestService_ResendVerification_Cooldown(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()

	test.repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{
				ID:                 verificationID,
				Status:             string(registration.VerificationRequestPending),
				RegistrationStatus: string(registration.PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(7 * 24 * time.Hour),
					Valid: true,
				},
				LastSentAt: pgtype.Timestamptz{
					Time:  time.Now().Add(-30 * time.Second),
					Valid: true,
				},
			},
			nil,
		)

	_, err := test.registrationService.ResendVerification(
		ctx,
		verificationID,
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	appErr := apperror.FromError(err)

	if appErr.Code != registration.CodeVerificationResendCooldown {
		t.Errorf(
			"expected error code %q, got %q",
			registration.CodeVerificationResendCooldown,
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

func TestService_ResendVerification_GetVerificationError(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()

	repositoryErr := apperror.NotFound(
		errors.New("verification not found"),
	)

	test.repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{},
			repositoryErr,
		)

	_, err := test.registrationService.ResendVerification(
		ctx,
		verificationID,
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

func TestService_ResendVerification_RepositoryError(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	verificationID := uuid.New()

	test.repository.
		EXPECT().
		GetVerification(ctx, verificationID).
		Return(
			registrationdb.GetVerificationRow{
				ID:                 verificationID,
				Status:             string(registration.VerificationRequestPending),
				RegistrationStatus: string(registration.PendingRegistrationPending),
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

	test.repository.
		EXPECT().
		IssueVerificationCode(
			ctx,
			gomock.Any(),
		).
		Return(
			registrationdb.VerificationRequest{},
			repositoryErr,
		)

	_, err := test.registrationService.ResendVerification(
		ctx,
		verificationID,
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