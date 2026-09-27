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
	"github.com/thoriqr/stash-it-backend/internal/security"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
)

func TestService_FinalizeSocialRegistration(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()

	continuationToken := "test-continuation-token"
	tokenHash := security.HashToken(continuationToken)

	continuationID := uuid.New()
	pendingRegistrationID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()

	metadata := session.SessionMetadata{
		Platform: "web",
		InstallationID: pgtype.UUID{
			Bytes: uuid.New(),
			Valid: true,
		},
		DeviceName: pgtype.Text{
			String: "Test Browser",
			Valid:  true,
		},
		UserAgent: pgtype.Text{
			String: "test-agent",
			Valid:  true,
		},
	}

	input := registration.FinalizeSocialRegistrationInput{
		ContinuationToken: continuationToken,
		DisplayName:       "John Doe",
	}

	testStartedAt := time.Now()
	continuationExpiresAt := testStartedAt.Add(15 * time.Minute)
	registrationExpiresAt := testStartedAt.Add(7 * 24 * time.Hour)

	test.repository.
		EXPECT().
		GetRegistrationContinuation(ctx, tokenHash).
		Return(
			registrationdb.GetRegistrationContinuationRow{
				ID:                    continuationID,
				PendingRegistrationID: pendingRegistrationID,
				Email:                 "john@example.com",
				RegistrationType:     string(registration.RegistrationTypeSocial),
				ExpiresAt: pgtype.Timestamptz{
					Time:  continuationExpiresAt,
					Valid: true,
				},
				ConsumedAt: pgtype.Timestamptz{
					Valid: false,
				},
				RegistrationStatus: string(
					registration.PendingRegistrationPending,
				),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  registrationExpiresAt,
					Valid: true,
				},
			},
			nil,
		)

	test.repository.
		EXPECT().
		FinalizeSocialRegistration(
			ctx,
			gomock.Any(),
		).
		DoAndReturn(func(
    		_ context.Context,
    		params registration.FinalizeSocialRegistrationParams,
		) (registrationdb.CreateUserRow, error) {
    		if params.PendingRegistrationID != pendingRegistrationID {
        		t.Errorf(
            		"expected pending registration ID %s, got %s",
            		pendingRegistrationID,
            		params.PendingRegistrationID,
        		)
    		}

    		if params.ContinuationID != continuationID {
        		t.Errorf(
            		"expected continuation ID %s, got %s",
            		continuationID,
            		params.ContinuationID,
        		)
    		}

    		if params.DisplayName != input.DisplayName {
        		t.Errorf(
            		"expected display name %q, got %q",
            		input.DisplayName,
            		params.DisplayName,
        		)
    		}

    		if !params.EmailVerifiedAt.Valid {
        		t.Error("expected email verified at to be valid")
    		}

    		if params.EmailVerifiedAt.Time.IsZero() {
        		t.Error("expected email verified at to be set")
    		}

    		return registrationdb.CreateUserRow{
        		ID:          userID,
        		Email:       "john@example.com",
        		DisplayName: input.DisplayName,
    		}, nil
		})

	sessionResult := session.CreateSessionResult{
		Session: sessiondb.Session{
			ID:     sessionID,
			UserID: userID,
		},
		RefreshToken: "refresh-token",
	}

	test.sessionCreator.
		EXPECT().
		CreateSession(ctx, userID, metadata).
		Return(sessionResult, nil)

	result, err := test.registrationService.FinalizeSocialRegistration(
		ctx,
		input,
		metadata,
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.UserID != userID {
		t.Errorf(
			"expected user ID %s, got %s",
			userID,
			result.UserID,
		)
	}

	if result.Email != "john@example.com" {
		t.Errorf(
			"expected email %q, got %q",
			"john@example.com",
			result.Email,
		)
	}

	if result.DisplayName != input.DisplayName {
		t.Errorf(
			"expected display name %q, got %q",
			input.DisplayName,
			result.DisplayName,
		)
	}

	if result.Session != sessionResult.Session {
		t.Errorf(
			"expected session %+v, got %+v",
			sessionResult.Session,
			result.Session,
		)
	}

	if result.RefreshToken != sessionResult.RefreshToken {
		t.Errorf(
			"expected refresh token %q, got %q",
			sessionResult.RefreshToken,
			result.RefreshToken,
		)
	}

	if result.AccessToken == "" {
		t.Fatal("expected access token")
	}
}

func TestService_FinalizeSocialRegistration_GetRegistrationContinuationError(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	continuationToken := "test-continuation-token"
	tokenHash := security.HashToken(continuationToken)

	repositoryErr := apperror.ConflictWith(
		"",
		"registration continuation is no longer available",
		nil,
	)

	test.repository.
		EXPECT().
		GetRegistrationContinuation(ctx, tokenHash).
		Return(
			registrationdb.GetRegistrationContinuationRow{},
			repositoryErr,
		)

	_, err := test.registrationService.FinalizeSocialRegistration(
		ctx,
		registration.FinalizeSocialRegistrationInput{
			ContinuationToken: continuationToken,
			DisplayName:       "John Doe",
		},
		session.SessionMetadata{},
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

func TestService_FinalizeSocialRegistration_ValidationError(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	continuationToken := "test-continuation-token"
	tokenHash := security.HashToken(continuationToken)

	test.repository.
		EXPECT().
		GetRegistrationContinuation(ctx, tokenHash).
		Return(
			registrationdb.GetRegistrationContinuationRow{
				ConsumedAt: pgtype.Timestamptz{
					Time:  time.Now(),
					Valid: true,
				},
				ExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(15 * time.Minute),
					Valid: true,
				},
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

	_, err := test.registrationService.FinalizeSocialRegistration(
		ctx,
		registration.FinalizeSocialRegistrationInput{
			ContinuationToken: continuationToken,
			DisplayName:       "John Doe",
		},
		session.SessionMetadata{},
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	appErr := apperror.FromError(err)

	if appErr.Code != registration.CodeRegistrationContinuationConsumed {
		t.Errorf(
			"expected error code %q, got %q",
			registration.CodeRegistrationContinuationConsumed,
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

func TestService_FinalizeSocialRegistration_InvalidRegistrationType(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	continuationToken := "test-continuation-token"
	tokenHash := security.HashToken(continuationToken)

	test.repository.
		EXPECT().
		GetRegistrationContinuation(ctx, tokenHash).
		Return(
			registrationdb.GetRegistrationContinuationRow{
				ID:                    uuid.New(),
				PendingRegistrationID: uuid.New(),
				Email:                 "john@example.com",
				RegistrationType:     string(registration.RegistrationTypeManual),
				ExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(15 * time.Minute),
					Valid: true,
				},
				ConsumedAt: pgtype.Timestamptz{
					Valid: false,
				},
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

	_, err := test.registrationService.FinalizeSocialRegistration(
		ctx,
		registration.FinalizeSocialRegistrationInput{
			ContinuationToken: continuationToken,
			DisplayName:       "John Doe",
		},
		session.SessionMetadata{},
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	appErr := apperror.FromError(err)

	if appErr.Code != registration.CodeInvalidRegistrationType {
		t.Errorf(
			"expected error code %q, got %q",
			registration.CodeInvalidRegistrationType,
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

func TestService_FinalizeSocialRegistration_RepositoryError(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()
	continuationToken := "test-continuation-token"
	tokenHash := security.HashToken(continuationToken)

	continuationID := uuid.New()
	pendingRegistrationID := uuid.New()

	test.repository.
		EXPECT().
		GetRegistrationContinuation(ctx, tokenHash).
		Return(
			registrationdb.GetRegistrationContinuationRow{
				ID:                    continuationID,
				PendingRegistrationID: pendingRegistrationID,
				Email:                 "john@example.com",
				RegistrationType:     string(registration.RegistrationTypeSocial),
				ExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(15 * time.Minute),
					Valid: true,
				},
				ConsumedAt: pgtype.Timestamptz{
					Valid: false,
				},
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

	repositoryErr := apperror.Internal(
		errors.New("database connection failed"),
	)

	test.repository.
		EXPECT().
		FinalizeSocialRegistration(
			ctx,
			gomock.Any(),
		).
		Return(
			registrationdb.CreateUserRow{},
			repositoryErr,
		)

	_, err := test.registrationService.FinalizeSocialRegistration(
		ctx,
		registration.FinalizeSocialRegistrationInput{
			ContinuationToken: continuationToken,
			DisplayName:       "John Doe",
		},
		session.SessionMetadata{},
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

func TestService_FinalizeSocialRegistration_SessionCreationError(t *testing.T) {
	test := newTestService(t)

	ctx := context.Background()

	continuationToken := "test-continuation-token"
	tokenHash := security.HashToken(continuationToken)

	continuationID := uuid.New()
	pendingRegistrationID := uuid.New()
	userID := uuid.New()

	metadata := session.SessionMetadata{}

	test.repository.
		EXPECT().
		GetRegistrationContinuation(ctx, tokenHash).
		Return(
			registrationdb.GetRegistrationContinuationRow{
				ID:                    continuationID,
				PendingRegistrationID: pendingRegistrationID,
				Email:                 "john@example.com",
				RegistrationType:     string(registration.RegistrationTypeSocial),
				ExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(15 * time.Minute),
					Valid: true,
				},
				ConsumedAt: pgtype.Timestamptz{
					Valid: false,
				},
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
		FinalizeSocialRegistration(
			ctx,
			gomock.Any(),
		).
		Return(
			registrationdb.CreateUserRow{
				ID:          userID,
				Email:       "john@example.com",
				DisplayName: "John Doe",
			},
			nil,
		)

	expectedErr := errors.New("session creation failed")

	test.sessionCreator.
		EXPECT().
		CreateSession(ctx, userID, metadata).
		Return(session.CreateSessionResult{}, expectedErr)

	_, err := test.registrationService.FinalizeSocialRegistration(
		ctx,
		registration.FinalizeSocialRegistrationInput{
			ContinuationToken: continuationToken,
			DisplayName:       "John Doe",
		},
		metadata,
	)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, expectedErr) {
		t.Fatalf(
			"expected error %v, got %v",
			expectedErr,
			err,
		)
	}
}