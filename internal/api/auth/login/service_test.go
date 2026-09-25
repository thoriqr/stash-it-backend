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
	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	registrationmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/registration/mocks"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	sessionmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/session/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

type mockGoogleTokenVerifier struct {
	identity login.GoogleIdentity
	err      error
}

func (m *mockGoogleTokenVerifier) Verify(
	ctx context.Context,
	idToken string,
) (login.GoogleIdentity, error) {
	return m.identity, m.err
}

func newTestService(
	t *testing.T,
) (
	*login.Service,
	*loginmocks.MockRepository,
	*sessionmocks.MockSessionCreator,
	*registrationmocks.MockSocialRegistrationService,
	*mockGoogleTokenVerifier,
	*security.PasswordHasher,
) {
	t.Helper()

	ctrl := gomock.NewController(t)

	repository := loginmocks.NewMockRepository(ctrl)
	sessionCreator := sessionmocks.NewMockSessionCreator(ctrl)
	registrationService := registrationmocks.NewMockSocialRegistrationService(ctrl)
	googleTokenVerifier := &mockGoogleTokenVerifier{}

	passwordHasher := security.NewPasswordHasher()
	accessTokenGenerator := security.NewAccessTokenGenerator(
		[]byte("test-secret"),
	)

	service := login.NewService(
		repository,
		sessionCreator,
		registrationService,
		googleTokenVerifier,
		passwordHasher,
		accessTokenGenerator,
	)

	return service,
		repository,
		sessionCreator,
		registrationService,
		googleTokenVerifier,
		passwordHasher
}

func TestService_LoginManual(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		service, repository, sessionCreator, _, _, passwordHasher := newTestService(t)

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
				Valid:  true,
			},
			UserAgent: pgtype.Text{
				String: "test-agent",
				Valid:  true,
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
		service, repository, _, _, _, _ := newTestService(t)

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
		service, repository, _, _, _, passwordHasher := newTestService(t)

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
		service, repository, sessionCreator, _, _, passwordHasher := newTestService(t)

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

func TestService_LoginGoogle(t *testing.T) {
	t.Run("authenticated", func(t *testing.T) {
		service, repository, sessionCreator, _, googleTokenVerifier, _ := newTestService(t)

		ctx := context.Background()
		idToken := "google-id-token"
		userID := uuid.New()
		sessionID := uuid.New()
		metadata := session.SessionMetadata{}

		googleTokenVerifier.identity = login.GoogleIdentity{
			Subject:       "google-subject-123",
			Email:         "user@example.com",
			EmailVerified: true,
			DisplayName:   "Test User",
		}

		authIdentity := logindb.GetAuthIdentityRow{
			ID:     uuid.New(),
			UserID: userID,
		}

		user := logindb.GetUserForLoginByIDRow{
			ID:          userID,
			Email:       "user@example.com",
			DisplayName: "Test User",
		}

		sessionResult := session.CreateSessionResult{
			Session: sessiondb.Session{
				ID:     sessionID,
				UserID: userID,
			},
			RefreshToken: "refresh-token",
		}

		repository.EXPECT().
			GetAuthIdentity(
				ctx,
				"google",
				"google-subject-123",
			).
			Return(authIdentity, nil)

		repository.EXPECT().
			GetUserForLoginByID(ctx, userID).
			Return(user, nil)

		sessionCreator.EXPECT().
			CreateSession(ctx, userID, metadata).
			Return(sessionResult, nil)

		result, err := service.LoginGoogle(
			ctx,
			idToken,
			metadata,
		)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if result.Outcome != login.LoginOutcomeAuthenticated {
			t.Fatalf(
				"expected outcome %q, got %q",
				login.LoginOutcomeAuthenticated,
				result.Outcome,
			)
		}

		if result.User == nil {
			t.Fatal("expected user")
		}

		if result.User.ID != user.ID {
			t.Fatalf(
				"expected user ID %s, got %s",
				user.ID,
				result.User.ID,
			)
		}

		if result.Session == nil {
			t.Fatal("expected session")
		}

		if result.Session.ID != sessionID {
			t.Fatalf(
				"expected session ID %s, got %s",
				sessionID,
				result.Session.ID,
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

	t.Run("account link required", func(t *testing.T) {
		service, repository, _, _, googleTokenVerifier, _ := newTestService(t)

		ctx := context.Background()
		idToken := "google-id-token"

		confirmationID := uuid.New()
		userID := uuid.New()

		googleTokenVerifier.identity = login.GoogleIdentity{
			Subject:       "google-subject-123",
			Email:         "user@example.com",
			EmailVerified: true,
			DisplayName:   "Google User",
		}

		repository.EXPECT().
			GetAuthIdentity(
				ctx,
				"google",
				"google-subject-123",
			).
			Return(logindb.GetAuthIdentityRow{}, nil)

		repository.EXPECT().
			GetUserByEmail(
				ctx,
				"user@example.com",
			).
			Return(
				logindb.GetUserByEmailRow{
					ID:          userID,
					Email:       "user@example.com",
					DisplayName: "Existing User",
				},
				nil,
			)

		repository.EXPECT().
			CreateAccountLinkConfirmation(
				ctx,
				gomock.Any(),
			).
			Return(
				logindb.AccountLinkConfirmation{
					ID: confirmationID,
				},
				nil,
			)

		result, err := service.LoginGoogle(
			ctx,
			idToken,
			session.SessionMetadata{},
		)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if result.Outcome != login.LoginOutcomeAccountLinkRequired {
			t.Fatalf(
				"expected outcome %q, got %q",
				login.LoginOutcomeAccountLinkRequired,
				result.Outcome,
			)
		}

		if result.ConfirmationID != confirmationID {
			t.Fatalf(
				"expected confirmation ID %s, got %s",
				confirmationID,
				result.ConfirmationID,
			)
		}
	})

	t.Run("registration required", func(t *testing.T) {
		service, repository, _, registrationService, googleTokenVerifier, _ := newTestService(t)

		ctx := context.Background()
		idToken := "google-id-token"

		verificationID := uuid.New()

		googleTokenVerifier.identity = login.GoogleIdentity{
			Subject:       "google-subject-123",
			Email:         "user@example.com",
			EmailVerified: true,
			DisplayName:   "Google User",
		}

		repository.EXPECT().
			GetAuthIdentity(
				ctx,
				"google",
				"google-subject-123",
			).
			Return(logindb.GetAuthIdentityRow{}, nil)

		repository.EXPECT().
			GetUserByEmail(
				ctx,
				"user@example.com",
			).
			Return(logindb.GetUserByEmailRow{}, nil)

		registrationService.EXPECT().
			CreateSocialRegistration(
				ctx,
				registration.CreateSocialRegistrationInput{
					Email:               "user@example.com",
					Provider:            "google",
					ProviderSubject:     "google-subject-123",
					EmailSnapshot: pgtype.Text{
						String: "user@example.com",
						Valid:  true,
					},
					DisplayNameSnapshot: pgtype.Text{
						String: "Google User",
						Valid:  true,
					},
				},
			).
			Return(verificationID, nil)

		result, err := service.LoginGoogle(
			ctx,
			idToken,
			session.SessionMetadata{},
		)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if result.Outcome != login.LoginOutcomeRegistrationRequired {
			t.Fatalf(
				"expected outcome %q, got %q",
				login.LoginOutcomeRegistrationRequired,
				result.Outcome,
			)
		}

		if result.VerificationID != verificationID {
			t.Fatalf(
				"expected verification ID %s, got %s",
				verificationID,
				result.VerificationID,
			)
		}
	})

	t.Run("google token verification error", func(t *testing.T) {
		service, _, _, _, googleTokenVerifier, _ := newTestService(t)

		ctx := context.Background()
		expectedErr := errors.New("invalid google token")

		googleTokenVerifier.err = expectedErr

		_, err := service.LoginGoogle(
			ctx,
			"invalid-token",
			session.SessionMetadata{},
		)

		if !errors.Is(err, expectedErr) {
			t.Fatalf(
				"expected error %v, got %v",
				expectedErr,
				err,
			)
		}
	})

	t.Run("auth identity repository error", func(t *testing.T) {
		service, repository, _, _, googleTokenVerifier, _ := newTestService(t)

		ctx := context.Background()
		expectedErr := errors.New("database unavailable")

		googleTokenVerifier.identity = login.GoogleIdentity{
			Subject:       "google-subject-123",
			Email:         "user@example.com",
			EmailVerified: true,
			DisplayName:   "Google User",
		}

		repository.EXPECT().
			GetAuthIdentity(
				ctx,
				"google",
				"google-subject-123",
			).
			Return(logindb.GetAuthIdentityRow{}, expectedErr)

		_, err := service.LoginGoogle(
			ctx,
			"google-id-token",
			session.SessionMetadata{},
		)

		if !errors.Is(err, expectedErr) {
			t.Fatalf(
				"expected error %v, got %v",
				expectedErr,
				err,
			)
		}
	})

	t.Run("user lookup repository error", func(t *testing.T) {
		service, repository, _, _, googleTokenVerifier, _ := newTestService(t)

		ctx := context.Background()
		expectedErr := errors.New("database unavailable")

		googleTokenVerifier.identity = login.GoogleIdentity{
			Subject:       "google-subject-123",
			Email:         "user@example.com",
			EmailVerified: true,
			DisplayName:   "Google User",
		}

		repository.EXPECT().
			GetAuthIdentity(
				ctx,
				"google",
				"google-subject-123",
			).
			Return(logindb.GetAuthIdentityRow{}, nil)

		repository.EXPECT().
			GetUserByEmail(
				ctx,
				"user@example.com",
			).
			Return(logindb.GetUserByEmailRow{}, expectedErr)

		_, err := service.LoginGoogle(
			ctx,
			"google-id-token",
			session.SessionMetadata{},
		)

		if !errors.Is(err, expectedErr) {
			t.Fatalf(
				"expected error %v, got %v",
				expectedErr,
				err,
			)
		}
	})

	t.Run("account link confirmation error", func(t *testing.T) {
		service, repository, _, _, googleTokenVerifier, _ := newTestService(t)

		ctx := context.Background()
		expectedErr := errors.New("failed to create account link confirmation")

		userID := uuid.New()

		googleTokenVerifier.identity = login.GoogleIdentity{
			Subject:       "google-subject-123",
			Email:         "user@example.com",
			EmailVerified: true,
			DisplayName:   "Google User",
		}

		repository.EXPECT().
			GetAuthIdentity(
				ctx,
				"google",
				"google-subject-123",
			).
			Return(logindb.GetAuthIdentityRow{}, nil)

		repository.EXPECT().
			GetUserByEmail(
				ctx,
				"user@example.com",
			).
			Return(
				logindb.GetUserByEmailRow{
					ID:          userID,
					Email:       "user@example.com",
					DisplayName: "Existing User",
				},
				nil,
			)

		repository.EXPECT().
			CreateAccountLinkConfirmation(
				ctx,
				gomock.Any(),
			).
			Return(logindb.AccountLinkConfirmation{}, expectedErr)

		_, err := service.LoginGoogle(
			ctx,
			"google-id-token",
			session.SessionMetadata{},
		)

		if !errors.Is(err, expectedErr) {
			t.Fatalf(
				"expected error %v, got %v",
				expectedErr,
				err,
			)
		}
	})

	t.Run("social registration error", func(t *testing.T) {
		service, repository, _, registrationService, googleTokenVerifier, _ := newTestService(t)

		ctx := context.Background()
		expectedErr := errors.New("failed to create social registration")

		googleTokenVerifier.identity = login.GoogleIdentity{
			Subject:       "google-subject-123",
			Email:         "user@example.com",
			EmailVerified: true,
			DisplayName:   "Google User",
		}

		repository.EXPECT().
			GetAuthIdentity(
				ctx,
				"google",
				"google-subject-123",
			).
			Return(logindb.GetAuthIdentityRow{}, nil)

		repository.EXPECT().
			GetUserByEmail(
				ctx,
				"user@example.com",
			).
			Return(logindb.GetUserByEmailRow{}, nil)

		registrationService.EXPECT().
			CreateSocialRegistration(
				ctx,
				registration.CreateSocialRegistrationInput{
					Email:               "user@example.com",
					Provider:            "google",
					ProviderSubject:     "google-subject-123",
					EmailSnapshot: pgtype.Text{
						String: "user@example.com",
						Valid:  true,
					},
					DisplayNameSnapshot: pgtype.Text{
						String: "Google User",
						Valid:  true,
					},
				},
			).
			Return(uuid.Nil, expectedErr)

		_, err := service.LoginGoogle(
			ctx,
			"google-id-token",
			session.SessionMetadata{},
		)

		if !errors.Is(err, expectedErr) {
			t.Fatalf(
				"expected error %v, got %v",
				expectedErr,
				err,
			)
		}
	})
}

func TestService_GetAccountLinkConfirmation(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		service, repository, _, _, _, _ := newTestService(t)

		ctx := context.Background()
		confirmationID := uuid.New()
		userID := uuid.New()

		repository.EXPECT().
			GetActiveAccountLinkConfirmation(ctx, confirmationID).
			Return(
				logindb.GetActiveAccountLinkConfirmationRow{
					ID:                  confirmationID,
					UserID:              userID,
					Provider:            "google",
					ProviderSubject:     "google-subject-123",
					EmailSnapshot:       pgtype.Text{
						String: "google@example.com",
						Valid:  true,
					},
					DisplayNameSnapshot: pgtype.Text{
						String: "Google User",
						Valid:  true,
					},
					UserEmail:       "user@example.com",
					UserDisplayName: "Existing User",
				},
				nil,
			)

		result, err := service.GetAccountLinkConfirmation(
			ctx,
			confirmationID,
		)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if result.ID != confirmationID {
			t.Fatalf(
				"expected ID %s, got %s",
				confirmationID,
				result.ID,
			)
		}

		if result.Provider != "google" {
			t.Fatalf(
				"expected provider %q, got %q",
				"google",
				result.Provider,
			)
		}

		if result.EmailSnapshot != "google@example.com" {
			t.Fatalf(
				"expected email snapshot %q, got %q",
				"google@example.com",
				result.EmailSnapshot,
			)
		}

		if result.DisplayNameSnapshot != "Google User" {
			t.Fatalf(
				"expected display name snapshot %q, got %q",
				"Google User",
				result.DisplayNameSnapshot,
			)
		}

		if result.UserEmail != "user@example.com" {
			t.Fatalf(
				"expected user email %q, got %q",
				"user@example.com",
				result.UserEmail,
			)
		}

		if result.UserDisplayName != "Existing User" {
			t.Fatalf(
				"expected user display name %q, got %q",
				"Existing User",
				result.UserDisplayName,
			)
		}
	})

	t.Run("repository error", func(t *testing.T) {
		service, repository, _, _, _, _ := newTestService(t)

		ctx := context.Background()
		confirmationID := uuid.New()
		expectedErr := errors.New("database unavailable")

		repository.EXPECT().
			GetActiveAccountLinkConfirmation(ctx, confirmationID).
			Return(
				logindb.GetActiveAccountLinkConfirmationRow{},
				expectedErr,
			)

		_, err := service.GetAccountLinkConfirmation(
			ctx,
			confirmationID,
		)

		if !errors.Is(err, expectedErr) {
			t.Fatalf(
				"expected error %v, got %v",
				expectedErr,
				err,
			)
		}
	})
}

func TestService_ConfirmAccountLink(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		service, repository, sessionCreator, _, _, _ := newTestService(t)

		ctx := context.Background()

		confirmationID := uuid.New()
		userID := uuid.New()
		sessionID := uuid.New()

		metadata := session.SessionMetadata{}

		repository.EXPECT().
			ConfirmAccountLink(ctx, confirmationID).
			Return(
				logindb.CreateAuthIdentityRow{
					ID:     uuid.New(),
					UserID: userID,
				},
				nil,
			)

		user := logindb.GetUserForLoginByIDRow{
			ID:          userID,
			Email:       "user@example.com",
			DisplayName: "Existing User",
		}

		repository.EXPECT().
			GetUserForLoginByID(ctx, userID).
			Return(user, nil)

		sessionResult := session.CreateSessionResult{
			Session: sessiondb.Session{
				ID:     sessionID,
				UserID: userID,
			},
			RefreshToken: "refresh-token",
		}

		sessionCreator.EXPECT().
			CreateSession(ctx, userID, metadata).
			Return(sessionResult, nil)

		result, err := service.ConfirmAccountLink(
			ctx,
			confirmationID,
			metadata,
		)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if result.Outcome != login.LoginOutcomeAuthenticated {
			t.Fatalf(
				"expected outcome %q, got %q",
				login.LoginOutcomeAuthenticated,
				result.Outcome,
			)
		}

		if result.User == nil {
			t.Fatal("expected user")
		}

		if result.User.ID != userID {
			t.Fatalf(
				"expected user ID %s, got %s",
				userID,
				result.User.ID,
			)
		}

		if result.Session == nil {
			t.Fatal("expected session")
		}

		if result.Session.ID != sessionID {
			t.Fatalf(
				"expected session ID %s, got %s",
				sessionID,
				result.Session.ID,
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

	t.Run("confirm account link error", func(t *testing.T) {
		service, repository, _, _, _, _ := newTestService(t)

		ctx := context.Background()

		confirmationID := uuid.New()
		expectedErr := errors.New("account link confirmation failed")

		repository.EXPECT().
			ConfirmAccountLink(ctx, confirmationID).
			Return(
				logindb.CreateAuthIdentityRow{},
				expectedErr,
			)

		_, err := service.ConfirmAccountLink(
			ctx,
			confirmationID,
			session.SessionMetadata{},
		)

		if !errors.Is(err, expectedErr) {
			t.Fatalf(
				"expected error %v, got %v",
				expectedErr,
				err,
			)
		}
	})

	t.Run("user lookup error", func(t *testing.T) {
		service, repository, _, _, _, _ := newTestService(t)

		ctx := context.Background()

		confirmationID := uuid.New()
		userID := uuid.New()
		expectedErr := errors.New("user lookup failed")

		repository.EXPECT().
			ConfirmAccountLink(ctx, confirmationID).
			Return(
				logindb.CreateAuthIdentityRow{
					ID:     uuid.New(),
					UserID: userID,
				},
				nil,
			)

		repository.EXPECT().
			GetUserForLoginByID(ctx, userID).
			Return(
				logindb.GetUserForLoginByIDRow{},
				expectedErr,
			)

		_, err := service.ConfirmAccountLink(
			ctx,
			confirmationID,
			session.SessionMetadata{},
		)

		if !errors.Is(err, expectedErr) {
			t.Fatalf(
				"expected error %v, got %v",
				expectedErr,
				err,
			)
		}
	})

	t.Run("session creation error", func(t *testing.T) {
		service, repository, sessionCreator, _, _, _ := newTestService(t)

		ctx := context.Background()

		confirmationID := uuid.New()
		userID := uuid.New()
		metadata := session.SessionMetadata{}
		expectedErr := errors.New("session creation failed")

		repository.EXPECT().
			ConfirmAccountLink(ctx, confirmationID).
			Return(
				logindb.CreateAuthIdentityRow{
					ID:     uuid.New(),
					UserID: userID,
				},
				nil,
			)

		repository.EXPECT().
			GetUserForLoginByID(ctx, userID).
			Return(
				logindb.GetUserForLoginByIDRow{
					ID:          userID,
					Email:       "user@example.com",
					DisplayName: "Existing User",
				},
				nil,
			)

		sessionCreator.EXPECT().
			CreateSession(ctx, userID, metadata).
			Return(
				session.CreateSessionResult{},
				expectedErr,
			)

		_, err := service.ConfirmAccountLink(
			ctx,
			confirmationID,
			metadata,
		)

		if !errors.Is(err, expectedErr) {
			t.Fatalf(
				"expected error %v, got %v",
				expectedErr,
				err,
			)
		}
	})
}