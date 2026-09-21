package login_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	logindb "github.com/thoriqr/stash-it-backend/internal/api/auth/login/generated"
	loginmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/login/mocks"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	sessionmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/session/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func TestService_LoginManual(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)

		repository := loginmocks.NewMockRepository(ctrl)
		sessionCreator := sessionmocks.NewMockSessionCreator(ctrl)

		passwordHasher := security.NewPasswordHasher()
		accessTokenGenerator := security.NewAccessTokenGenerator(
			[]byte("test-secret"),
		)

		service := login.NewService(
			repository,
			sessionCreator,
			passwordHasher,
			accessTokenGenerator,
		)

		ctx := context.Background()

		userID := uuid.New()
		sessionID := uuid.New()
		installationID := uuid.New()

		password := "correct-password"

		passwordHash, err := passwordHasher.Hash(password)
		if err != nil {
			t.Fatalf("failed to hash password: %v", err)
		}

		user := logindb.GetUserForLoginRow{
			ID:           userID,
			Email:        "user@example.com",
			DisplayName:  "Test User",
			PasswordHash: passwordHash,
		}

		metadata := session.SessionMetadata{
    Platform: "web",
    InstallationID: pgtype.UUID{
        Bytes: installationID,
        Valid: true,
    },
    DeviceName: pgtype.Text{
        String: "Test Browser",
        Valid: true,
    },
    UserAgent: pgtype.Text{
        String: "test-agent",
        Valid: true,
    },
}

		sessionResult := session.CreateSessionResult{
			Session: sessiondb.Session{
				ID:     sessionID,
				UserID: userID,
			},
			RefreshToken: "refresh-token",
		}

		repository.EXPECT().
			GetUserForLogin(ctx, "user@example.com").
			Return(user, nil)

		sessionCreator.EXPECT().
			CreateSession(ctx, userID, metadata).
			Return(sessionResult, nil)

		result, err := service.LoginManual(
			ctx,
			"user@example.com",
			password,
			metadata,
		)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if result.User != user {
			t.Fatalf("expected user %+v, got %+v", user, result.User)
		}

		if result.Session != sessionResult.Session {
			t.Fatalf(
				"expected session %+v, got %+v",
				sessionResult.Session,
				result.Session,
			)
		}

		if result.RefreshToken != sessionResult.RefreshToken {
			t.Fatalf(
				"expected refresh token %q, got %q",
				sessionResult.RefreshToken,
				result.RefreshToken,
			)
		}

		if result.AccessToken == "" {
			t.Fatal("expected access token")
		}
	})

	t.Run("repository error", func(t *testing.T) {
		ctrl := gomock.NewController(t)

		repository := loginmocks.NewMockRepository(ctrl)
		sessionCreator := sessionmocks.NewMockSessionCreator(ctrl)

		passwordHasher := security.NewPasswordHasher()
		accessTokenGenerator := security.NewAccessTokenGenerator(
			[]byte("test-secret"),
		)

		service := login.NewService(
			repository,
			sessionCreator,
			passwordHasher,
			accessTokenGenerator,
		)

		ctx := context.Background()
		expectedErr := errors.New("database unavailable")

		repository.EXPECT().
			GetUserForLogin(ctx, "user@example.com").
			Return(logindb.GetUserForLoginRow{}, expectedErr)

		_, err := service.LoginManual(
			ctx,
			"user@example.com",
			"correct-password",
			session.SessionMetadata{},
		)

		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected error %v, got %v", expectedErr, err)
		}
	})

	t.Run("invalid password", func(t *testing.T) {
		ctrl := gomock.NewController(t)

		repository := loginmocks.NewMockRepository(ctrl)
		sessionCreator := sessionmocks.NewMockSessionCreator(ctrl)

		passwordHasher := security.NewPasswordHasher()
		accessTokenGenerator := security.NewAccessTokenGenerator(
			[]byte("test-secret"),
		)

		service := login.NewService(
			repository,
			sessionCreator,
			passwordHasher,
			accessTokenGenerator,
		)

		ctx := context.Background()

		passwordHash, err := passwordHasher.Hash("correct-password")
		if err != nil {
			t.Fatalf("failed to hash password: %v", err)
		}

		user := logindb.GetUserForLoginRow{
			ID:           uuid.New(),
			Email:        "user@example.com",
			DisplayName:  "Test User",
			PasswordHash: passwordHash,
		}

		repository.EXPECT().
			GetUserForLogin(ctx, "user@example.com").
			Return(user, nil)

		_, err = service.LoginManual(
			ctx,
			"user@example.com",
			"wrong-password",
			session.SessionMetadata{},
		)

		if err == nil {
			t.Fatal("expected error")
		}

		appErr := apperror.FromError(err)

		if appErr.Status != http.StatusUnauthorized {
			t.Fatalf(
				"expected status %d, got %d",
				http.StatusUnauthorized,
				appErr.Status,
			)
		}

		if appErr.Code != login.CodeInvalidCredentials {
			t.Fatalf(
				"expected code %s, got %s",
				login.CodeInvalidCredentials,
				appErr.Code,
			)
		}
	})

	t.Run("session creation error", func(t *testing.T) {
		ctrl := gomock.NewController(t)

		repository := loginmocks.NewMockRepository(ctrl)
		sessionCreator := sessionmocks.NewMockSessionCreator(ctrl)

		passwordHasher := security.NewPasswordHasher()
		accessTokenGenerator := security.NewAccessTokenGenerator(
		[]byte("test-secret"),
		)

		service := login.NewService(
			repository,
			sessionCreator,
			passwordHasher,
			accessTokenGenerator,
		)

		ctx := context.Background()
		userID := uuid.New()
		metadata := session.SessionMetadata{}

		passwordHash, err := passwordHasher.Hash("correct-password")
		if err != nil {
			t.Fatalf("failed to hash password: %v", err)
		}

		user := logindb.GetUserForLoginRow{
			ID:           userID,
			Email:        "user@example.com",
			DisplayName:  "Test User",
			PasswordHash: passwordHash,
		}

		expectedErr := errors.New("session creation failed")

		repository.EXPECT().
			GetUserForLogin(ctx, "user@example.com").
			Return(user, nil)

		sessionCreator.EXPECT().
			CreateSession(ctx, userID, metadata).
			Return(session.CreateSessionResult{}, expectedErr)

		_, err = service.LoginManual(
			ctx,
			"user@example.com",
			"correct-password",
			metadata,
		)

		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected error %v, got %v", expectedErr, err)
		}
	})
}
