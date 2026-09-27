package registration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	registrationdb "github.com/thoriqr/stash-it-backend/internal/api/auth/registration/generated"
)

func TestService_CreateSocialRegistration(t *testing.T) {
    test := newTestService(t)

    ctx := context.Background()

    verificationID := uuid.New()

    emailSnapshot := pgtype.Text{
        String: "google@example.com",
        Valid:  true,
    }

    displayNameSnapshot := pgtype.Text{
        String: "Google User",
        Valid:  true,
    }

    testStartedAt := time.Now()

    test.repository.
        EXPECT().
        CreateSocialRegistration(
            ctx,
            gomock.Any(),
        ).
        DoAndReturn(func(
            _ context.Context,
            params registration.CreateSocialRegistrationParams,
        ) (registration.CreateSocialRegistrationResult, error) {
            if params.Email != "test@example.com" {
                t.Errorf(
                    "expected normalized email %q, got %q",
                    "test@example.com",
                    params.Email,
                )
            }

            if params.Provider != "google" {
                t.Errorf(
                    "expected provider %q, got %q",
                    "google",
                    params.Provider,
                )
            }

            if params.ProviderSubject != "google-subject-123" {
                t.Errorf(
                    "expected provider subject %q, got %q",
                    "google-subject-123",
                    params.ProviderSubject,
                )
            }

            if params.EmailSnapshot != emailSnapshot {
                t.Errorf(
                    "expected email snapshot %+v, got %+v",
                    emailSnapshot,
                    params.EmailSnapshot,
                )
            }

            if params.DisplayNameSnapshot != displayNameSnapshot {
                t.Errorf(
                    "expected display name snapshot %+v, got %+v",
                    displayNameSnapshot,
                    params.DisplayNameSnapshot,
                )
            }

            if !params.RegistrationExpiresAt.Valid {
                t.Error("expected registration expiration to be valid")
            }

            now := time.Now()

            registrationMin := testStartedAt.Add(7 * 24 * time.Hour)
            registrationMax := now.Add(7 * 24 * time.Hour)

            if params.RegistrationExpiresAt.Time.Before(registrationMin) ||
                params.RegistrationExpiresAt.Time.After(registrationMax) {
                t.Errorf(
                    "unexpected registration expiration: %v",
                    params.RegistrationExpiresAt.Time,
                )
            }

            return registration.CreateSocialRegistrationResult{
                VerificationRequest: registrationdb.VerificationRequest{
                    ID: verificationID,
                },
            }, nil
        })

    result, err := test.socialService.CreateSocialRegistration(
        ctx,
        registration.CreateSocialRegistrationInput{
            Email:               "  Test@Example.COM  ",
            Provider:            "google",
            ProviderSubject:     "google-subject-123",
            EmailSnapshot:       emailSnapshot,
            DisplayNameSnapshot: displayNameSnapshot,
        },
    )

    if err != nil {
        t.Fatalf("expected no error, got %v", err)
    }

    if result != verificationID {
        t.Errorf(
            "expected verification ID %s, got %s",
            verificationID,
            result,
        )
    }

		t.Run("RepositoryError", func(t *testing.T) {
    expectedErr := errors.New("repository error")

    test.repository.EXPECT().
        CreateSocialRegistration(ctx, gomock.Any()).
        Return(registration.CreateSocialRegistrationResult{}, expectedErr)

    result, err := test.socialService.CreateSocialRegistration(ctx, registration.CreateSocialRegistrationInput{
        Email:               "test@example.com",
        Provider:            "google",
        ProviderSubject:     "google-subject",
        EmailSnapshot:       emailSnapshot,
        DisplayNameSnapshot: displayNameSnapshot,
    })

    require.ErrorIs(t, err, expectedErr)
    require.Equal(t, uuid.Nil, result)
})
}