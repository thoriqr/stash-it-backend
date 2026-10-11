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
	"github.com/thoriqr/stash-it-backend/internal/testutil"
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

	passwordHasher := testutil.NewPasswordHasher()
	accessTokenGenerator := security.NewAccessTokenGenerator(
		[]byte("test-secret"),
	)

	rateLimiter := testutil.NewPermissivePinRateLimiter()

	service := login.NewService(
		repository,
		sessionCreator,
		registrationService,
		googleTokenVerifier,
		passwordHasher,
		accessTokenGenerator,
		rateLimiter,
	)

	return service,
		repository,
		sessionCreator,
		registrationService,
		googleTokenVerifier,
		passwordHasher
}

// newTestServiceWithLimiter builds a test service with a custom rate limiter.
func newTestServiceWithLimiter(
	t *testing.T,
	rateLimiter login.LoginRateLimiter,
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

	passwordHasher := testutil.NewPasswordHasher()
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
		rateLimiter,
	)

	return service,
		repository,
		sessionCreator,
		registrationService,
		googleTokenVerifier,
		passwordHasher
}

// The address login looks up is the one registration stored. The comparison is
// exact, so a mixed-case or padded address that is not normalized here finds
// no user and is reported as invalid credentials, which is indistinguishable
// from a wrong password.
func TestService_LoginManual_NormalizesEmail(t *testing.T) {
	submitted := map[string]string{
		"mixed case":        "  Alice@Example.COM ",
		"mixed case alone":  "Alice@Example.COM",
		"surrounding space": "  alice@example.com  ",
		"already stored":    "alice@example.com",
	}

	for name, submittedEmail := range submitted {
		t.Run(name, func(t *testing.T) {
			service, repository, sessionCreator, _, _, passwordHasher :=
				newTestService(t)

			ctx := context.Background()
			userID := uuid.New()

			passwordHash, err := passwordHasher.Hash(context.Background(), "correct-password")
			if err != nil {
				t.Fatalf("failed to hash password: %v", err)
			}

			// The assertion is on the argument: gomock fails this test if the
			// lookup is made with anything but the stored form.
			repository.EXPECT().
				GetUserForLogin(ctx, "alice@example.com").
				Return(logindb.GetUserForLoginRow{
					ID:           userID,
					Email:        "alice@example.com",
					DisplayName:  "Alice",
					PasswordHash: passwordHash,
				}, nil)

			sessionCreator.EXPECT().
				CreateSession(ctx, userID, session.SessionMetadata{}).
				Return(session.CreateSessionResult{
					Session:      sessiondb.Session{ID: uuid.New(), UserID: userID},
					RefreshToken: "refresh-token",
				}, nil)

			result, err := service.LoginManual(
				ctx,
				submittedEmail,
				"correct-password",
				session.SessionMetadata{},
			)

			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}

			if result.User.ID != userID {
				t.Fatalf("expected user %s, got %+v", userID, result.User)
			}
		})
	}
}

func TestService_LoginGoogle_NormalizesEmail(t *testing.T) {
	service, repository, _, _, googleTokenVerifier, _ := newTestService(t)

	ctx := context.Background()

	// Google's claim is used exactly as issued, casing and all.
	googleTokenVerifier.identity = login.GoogleIdentity{
		Subject:       "google-subject-123",
		Email:         "Alice@Example.com",
		EmailVerified: true,
		DisplayName:   "Alice",
	}

	// No linked Google identity yet, so the flow falls through to the email
	// lookup, which is the call under test.
	repository.EXPECT().
		GetAuthIdentity(ctx, "google", "google-subject-123").
		Return(logindb.GetAuthIdentityRow{}, nil)

	// The lookup must be made against the form the address is stored in, so an
	// existing user is found and offered account linking rather than being told
	// to register again.
	repository.EXPECT().
		GetUserByEmail(ctx, "alice@example.com").
		Return(logintestdbRowWithID(), nil)

	// Reaching the account-link branch is the proof the user was found.
	repository.EXPECT().
		CreateAccountLinkConfirmation(ctx, gomock.Any()).
		Return(logindb.AccountLinkConfirmation{ID: uuid.New()}, nil)

	result, err := service.LoginGoogle(
		ctx,
		"id-token",
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
}

// logintestdbRowWithID is a minimal existing-user row: a non-nil id is what tells
// the service a user already holds the address.
func logintestdbRowWithID() logindb.GetUserByEmailRow {
	return logindb.GetUserByEmailRow{
		ID:          uuid.New(),
		Email:       "alice@example.com",
		DisplayName: "Alice",
	}
}

func TestService_LoginManual(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		service, repository, sessionCreator, _, _, passwordHasher := newTestService(t)

		ctx := context.Background()

		userID := uuid.New()
		sessionID := uuid.New()
		installationID := uuid.New()

		password := "correct-password"

		passwordHash, err := passwordHasher.Hash(context.Background(), password)
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

		passwordHash, err := passwordHasher.Hash(context.Background(), "correct-password")
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

		passwordHash, err := passwordHasher.Hash(context.Background(), "correct-password")
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
					Email:           "user@example.com",
					Provider:        "google",
					ProviderSubject: "google-subject-123",
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
					Email:           "user@example.com",
					Provider:        "google",
					ProviderSubject: "google-subject-123",
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
					ID:              confirmationID,
					UserID:          userID,
					Provider:        "google",
					ProviderSubject: "google-subject-123",
					DisplayNameSnapshot: pgtype.Text{
						String: "Google User",
						Valid:  true,
					},
					UserEmail: "user@example.com",
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

		// The Google profile name is reported as captured at creation time.
		if result.DisplayNameSnapshot != "Google User" {
			t.Fatalf(
				"expected display name snapshot %q, got %q",
				"Google User",
				result.DisplayNameSnapshot,
			)
		}

		// The account's address, redacted. The Google snapshot deliberately
		// differs from it in the fixture above, which is what makes this
		// assertion able to tell the two sources apart.
		if result.MaskedUserEmail != "u***@example.com" {
			t.Fatalf(
				"expected masked email %q, got %q",
				"u***@example.com",
				result.MaskedUserEmail,
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

// activeConfirmation is the row the repository returns for a live confirmation,
// carrying an identity the tests then match or deliberately fail to match.
func activeConfirmation(
	confirmationID uuid.UUID,
	userID uuid.UUID,
	provider string,
	providerSubject string,
) logindb.GetActiveAccountLinkConfirmationRow {
	return logindb.GetActiveAccountLinkConfirmationRow{
		ID:                  confirmationID,
		UserID:              userID,
		Provider:            provider,
		ProviderSubject:     providerSubject,
		DisplayNameSnapshot: pgtype.Text{String: "Alice", Valid: true},
		UserEmail:           "alice@example.com",
	}
}

func TestService_ConfirmAccountLink(t *testing.T) {
	const (
		googleIDToken = "google-id-token"
		subject       = "google-subject-123"
	)

	// The happy path. The token's identity is the one the confirmation was
	// created for, so the link is written and the caller is signed in.
	t.Run("matching identity succeeds", func(t *testing.T) {
		service, repository, sessionCreator, _, googleTokenVerifier, _ :=
			newTestService(t)

		ctx := context.Background()
		confirmationID := uuid.New()
		userID := uuid.New()

		googleTokenVerifier.identity = login.GoogleIdentity{
			Subject:       subject,
			Email:         "alice@example.com",
			EmailVerified: true,
		}

		repository.EXPECT().
			GetActiveAccountLinkConfirmation(ctx, confirmationID).
			Return(activeConfirmation(confirmationID, userID, "google", subject), nil)

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
					Email:       "alice@example.com",
					DisplayName: "Alice",
				},
				nil,
			)

		sessionCreator.EXPECT().
			CreateSession(ctx, userID, gomock.Any()).
			Return(
				session.CreateSessionResult{
					Session:      sessiondb.Session{ID: uuid.New(), UserID: userID},
					RefreshToken: "refresh-token",
				},
				nil,
			)

		result, err := service.ConfirmAccountLink(
			ctx,
			confirmationID,
			googleIDToken,
			session.SessionMetadata{},
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

		if result.User == nil || result.User.ID != userID {
			t.Fatalf("expected the linked user, got %+v", result.User)
		}

		if result.AccessToken == "" || result.RefreshToken == "" {
			t.Fatal("expected an access token and a refresh token")
		}
	})

	// An invalid or expired token is refused before anything is read, so it
	// cannot link an identity or create a session, and it cannot even tell the
	// caller whether the confirmation exists.
	//
	// No repository expectation is registered for this case. gomock fails the
	// test if any is reached, which is the assertion: an unauthenticated caller
	// costs a signature verification and nothing else.
	t.Run("invalid token links nothing", func(t *testing.T) {
		service, _, _, _, googleTokenVerifier, _ := newTestService(t)

		googleTokenVerifier.err = apperror.UnauthorizedWith(
			login.CodeInvalidGoogleToken,
			"invalid Google ID token",
			errors.New("token expired"),
		)

		_, err := service.ConfirmAccountLink(
			context.Background(),
			uuid.New(),
			"expired-token",
			session.SessionMetadata{},
		)
		if err == nil {
			t.Fatal("expected an error")
		}

		appErr := apperror.FromError(err)

		if appErr.Code != login.CodeInvalidGoogleToken {
			t.Fatalf(
				"expected code %s, got %s",
				login.CodeInvalidGoogleToken,
				appErr.Code,
			)
		}

		if appErr.Status != http.StatusUnauthorized {
			t.Fatalf(
				"expected status %d, got %d",
				http.StatusUnauthorized,
				appErr.Status,
			)
		}
	})

	// The confirmation names the identity that started the flow. A valid token
	// for a different Google account is refused, and refused separately from an
	// invalid token so the caller learns that re-prompting will not help.
	//
	// The repository's ConfirmAccountLink is deliberately not expected: matching
	// is refused before anything is consumed.
	t.Run("different subject is rejected", func(t *testing.T) {
		service, repository, _, _, googleTokenVerifier, _ := newTestService(t)

		ctx := context.Background()
		confirmationID := uuid.New()

		// Same address as the confirmation, different Google account. This is the
		// case where email equality alone would wrongly succeed.
		googleTokenVerifier.identity = login.GoogleIdentity{
			Subject:       "a-completely-different-google-account",
			Email:         "alice@example.com",
			EmailVerified: true,
		}

		repository.EXPECT().
			GetActiveAccountLinkConfirmation(ctx, confirmationID).
			Return(activeConfirmation(confirmationID, uuid.New(), "google", subject), nil)

		_, err := service.ConfirmAccountLink(
			ctx,
			confirmationID,
			googleIDToken,
			session.SessionMetadata{},
		)
		if err == nil {
			t.Fatal("expected an error")
		}

		appErr := apperror.FromError(err)

		if appErr.Code != login.CodeAccountLinkIdentityMismatch {
			t.Fatalf(
				"expected code %s, got %s",
				login.CodeAccountLinkIdentityMismatch,
				appErr.Code,
			)
		}

		if appErr.Status != http.StatusConflict {
			t.Fatalf(
				"expected status %d, got %d",
				http.StatusConflict,
				appErr.Status,
			)
		}

		// The message must not describe the stored identity or the account it
		// points at, or a mismatched caller learns something from being refused.
		if appErr.Message != "account link confirmation does not match this Google account" {
			t.Fatalf("unexpected message %q", appErr.Message)
		}
	})

	// Provider is data, not code. It is read back out of the row and written
	// into auth_identities, so a row naming another provider must not be
	// satisfied by a token from this one.
	t.Run("different provider is rejected", func(t *testing.T) {
		service, repository, _, _, googleTokenVerifier, _ := newTestService(t)

		ctx := context.Background()
		confirmationID := uuid.New()

		googleTokenVerifier.identity = login.GoogleIdentity{
			Subject:       subject,
			Email:         "alice@example.com",
			EmailVerified: true,
		}

		// Identical subject, different provider.
		repository.EXPECT().
			GetActiveAccountLinkConfirmation(ctx, confirmationID).
			Return(
				activeConfirmation(confirmationID, uuid.New(), "apple", subject),
				nil,
			)

		_, err := service.ConfirmAccountLink(
			ctx,
			confirmationID,
			googleIDToken,
			session.SessionMetadata{},
		)
		if err == nil {
			t.Fatal("expected an error")
		}

		if apperror.FromError(err).Code != login.CodeAccountLinkIdentityMismatch {
			t.Fatalf(
				"expected code %s, got %s",
				login.CodeAccountLinkIdentityMismatch,
				apperror.FromError(err).Code,
			)
		}
	})

	// The comparison is made against what the confirmation stores, never
	// against the caller's token. A token whose subject matches nothing in the
	// row is refused however plausible its email is.
	t.Run("confirmation row is the authority, not the token", func(t *testing.T) {
		service, repository, _, _, googleTokenVerifier, _ := newTestService(t)

		ctx := context.Background()
		confirmationID := uuid.New()

		googleTokenVerifier.identity = login.GoogleIdentity{
			Subject:       "subject-from-the-token",
			Email:         "someone-else@example.com",
			EmailVerified: true,
		}

		repository.EXPECT().
			GetActiveAccountLinkConfirmation(ctx, confirmationID).
			Return(
				activeConfirmation(
					confirmationID,
					uuid.New(),
					"google",
					"subject-stored-on-the-confirmation",
				),
				nil,
			)

		_, err := service.ConfirmAccountLink(
			ctx,
			confirmationID,
			googleIDToken,
			session.SessionMetadata{},
		)
		if err == nil {
			t.Fatal("expected an error")
		}

		if apperror.FromError(err).Code != login.CodeAccountLinkIdentityMismatch {
			t.Fatalf("expected a mismatch, got %s", apperror.FromError(err).Code)
		}
	})

	// A confirmation that is unknown, expired or already used is refused before
	// the comparison, so the response does not distinguish those three cases.
	t.Run("inactive confirmation is rejected", func(t *testing.T) {
		service, repository, _, _, googleTokenVerifier, _ := newTestService(t)

		ctx := context.Background()
		confirmationID := uuid.New()

		googleTokenVerifier.identity = login.GoogleIdentity{
			Subject: subject,
			Email:   "alice@example.com",
		}

		repository.EXPECT().
			GetActiveAccountLinkConfirmation(ctx, confirmationID).
			Return(
				logindb.GetActiveAccountLinkConfirmationRow{},
				apperror.ConflictWith(
					login.CodeAccountLinkConfirmationInvalid,
					"account link confirmation is invalid",
					nil,
				),
			)

		_, err := service.ConfirmAccountLink(
			ctx,
			confirmationID,
			googleIDToken,
			session.SessionMetadata{},
		)
		if err == nil {
			t.Fatal("expected an error")
		}

		if apperror.FromError(err).Code != login.CodeAccountLinkConfirmationInvalid {
			t.Fatalf("expected an invalid confirmation, got %s", apperror.FromError(err).Code)
		}
	})

	// The transaction's own refusal is passed through unchanged.
	t.Run("confirm account link error", func(t *testing.T) {
		service, repository, _, _, googleTokenVerifier, _ := newTestService(t)

		ctx := context.Background()
		confirmationID := uuid.New()
		expectedErr := errors.New("account link confirmation failed")

		googleTokenVerifier.identity = login.GoogleIdentity{Subject: subject}

		repository.EXPECT().
			GetActiveAccountLinkConfirmation(ctx, confirmationID).
			Return(activeConfirmation(confirmationID, uuid.New(), "google", subject), nil)

		repository.EXPECT().
			ConfirmAccountLink(ctx, confirmationID).
			Return(logindb.CreateAuthIdentityRow{}, expectedErr)

		_, err := service.ConfirmAccountLink(
			ctx,
			confirmationID,
			googleIDToken,
			session.SessionMetadata{},
		)
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected error %v, got %v", expectedErr, err)
		}
	})

	// A user that vanished between the commit and the read is reported the same
	// way as any other post-commit failure, for the same reason: the link is
	// written and the caller has no session.
	t.Run("user lookup error after commit", func(t *testing.T) {
		service, repository, _, _, googleTokenVerifier, _ := newTestService(t)

		ctx := context.Background()
		confirmationID := uuid.New()
		userID := uuid.New()
		expectedErr := errors.New("user lookup failed")

		googleTokenVerifier.identity = login.GoogleIdentity{Subject: subject}

		repository.EXPECT().
			GetActiveAccountLinkConfirmation(ctx, confirmationID).
			Return(activeConfirmation(confirmationID, userID, "google", subject), nil)

		repository.EXPECT().
			ConfirmAccountLink(ctx, confirmationID).
			Return(logindb.CreateAuthIdentityRow{ID: uuid.New(), UserID: userID}, nil)

		repository.EXPECT().
			GetUserForLoginByID(ctx, userID).
			Return(logindb.GetUserForLoginByIDRow{}, expectedErr)

		_, err := service.ConfirmAccountLink(
			ctx,
			confirmationID,
			googleIDToken,
			session.SessionMetadata{},
		)
		if err == nil {
			t.Fatal("expected an error")
		}

		appErr := apperror.FromError(err)

		if appErr.Code != login.CodeAccountLinkSessionFailed {
			t.Fatalf("expected code %s, got %s", login.CodeAccountLinkSessionFailed, appErr.Code)
		}

		if appErr.Status != http.StatusServiceUnavailable {
			t.Fatalf(
				"expected status %d, got %d",
				http.StatusServiceUnavailable,
				appErr.Status,
			)
		}
	})

	// Session creation fails after the link is committed. The confirmation is
	// spent and retrying it would report the confirmation as invalid, so the
	// caller is told to retry Google login instead — which works, because the
	// identity is now linked.
	t.Run("session creation error after commit is actionable", func(t *testing.T) {
		service, repository, sessionCreator, _, googleTokenVerifier, _ :=
			newTestService(t)

		ctx := context.Background()
		confirmationID := uuid.New()
		userID := uuid.New()
		expectedErr := errors.New("session creation failed")

		googleTokenVerifier.identity = login.GoogleIdentity{Subject: subject}

		repository.EXPECT().
			GetActiveAccountLinkConfirmation(ctx, confirmationID).
			Return(activeConfirmation(confirmationID, userID, "google", subject), nil)

		repository.EXPECT().
			ConfirmAccountLink(ctx, confirmationID).
			Return(logindb.CreateAuthIdentityRow{ID: uuid.New(), UserID: userID}, nil)

		repository.EXPECT().
			GetUserForLoginByID(ctx, userID).
			Return(
				logindb.GetUserForLoginByIDRow{
					ID:    userID,
					Email: "alice@example.com",
				},
				nil,
			)

		sessionCreator.EXPECT().
			CreateSession(ctx, userID, gomock.Any()).
			Return(session.CreateSessionResult{}, expectedErr)

		_, err := service.ConfirmAccountLink(
			ctx,
			confirmationID,
			googleIDToken,
			session.SessionMetadata{},
		)
		if err == nil {
			t.Fatal("expected an error")
		}

		appErr := apperror.FromError(err)

		if appErr.Code != login.CodeAccountLinkSessionFailed {
			t.Fatalf("expected code %s, got %s", login.CodeAccountLinkSessionFailed, appErr.Code)
		}

		if !errors.Is(err, expectedErr) {
			t.Fatalf("the underlying fault must be retained, got %v", err)
		}

		if appErr.Message == "" {
			t.Fatal("a 503 the client cannot act on is not actionable")
		}
	})
}
