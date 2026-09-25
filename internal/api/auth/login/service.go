package login

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	logindb "github.com/thoriqr/stash-it-backend/internal/api/auth/login/generated"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

type Service struct {
    repository          Repository
    sessionService      session.SessionCreator
    registrationService registration.SocialRegistrationService
    googleTokenVerifier GoogleTokenVerifier
    passwordHasher      *security.PasswordHasher
    accessTokenGenerator *security.AccessTokenGenerator
}

func NewService(
    repository Repository,
    sessionService session.SessionCreator,
    registrationService registration.SocialRegistrationService,
    googleTokenVerifier GoogleTokenVerifier,
    passwordHasher *security.PasswordHasher,
    accessTokenGenerator *security.AccessTokenGenerator,
) *Service {
    return &Service{
        repository:          repository,
        sessionService:      sessionService,
        registrationService: registrationService,
        googleTokenVerifier: googleTokenVerifier,
        passwordHasher:      passwordHasher,
        accessTokenGenerator: accessTokenGenerator,
    }
}

type LoginResult struct {
	User         logindb.GetUserForLoginRow
	Session      sessiondb.Session
	AccessToken  string
	RefreshToken string
}

func (s *Service) LoginManual(
    ctx context.Context,
    email string,
    password string,
    metadata session.SessionMetadata,
) (LoginResult, error) {
    user, err := s.repository.GetUserForLogin(ctx, email)
    if err != nil {
        return LoginResult{}, err
    }

    result, err := s.passwordHasher.Verify(password, user.PasswordHash)
    if err != nil {
        return LoginResult{}, apperror.Internal(err)
    }

    if !result.Match {
        return LoginResult{}, apperror.UnauthorizedWith(
            CodeInvalidCredentials,
            "invalid email or password",
            nil,
        )
    }

    sessionResult, err := s.sessionService.CreateSession(
        ctx,
        user.ID,
        metadata,
    )
    if err != nil {
        return LoginResult{}, err
    }

    accessToken, err := s.accessTokenGenerator.Generate(
        user.ID,
        sessionResult.Session.ID,
        session.AccessTokenLifetime,
    )
    if err != nil {
        return LoginResult{}, apperror.Internal(err)
    }

    return LoginResult{
        User:         user,
        Session:      sessionResult.Session,
        AccessToken:  accessToken,
        RefreshToken: sessionResult.RefreshToken,
    }, nil
}

type LoginGoogleResult struct {
    Outcome LoginOutcome

    VerificationID uuid.UUID
    ConfirmationID uuid.UUID

    User         *logindb.GetUserForLoginByIDRow
    Session      *sessiondb.Session
    AccessToken  string
    RefreshToken string
}

func (s *Service) LoginGoogle(
	ctx context.Context,
	idToken string,
	metadata session.SessionMetadata,
) (LoginGoogleResult, error) {
	identity, err := s.googleTokenVerifier.Verify(
		ctx,
		idToken,
	)
	if err != nil {
		return LoginGoogleResult{}, err
	}

	authIdentity, err := s.repository.GetAuthIdentity(
		ctx,
		"google",
		identity.Subject,
	)
	if err != nil {
		return LoginGoogleResult{}, err
	}

	if authIdentity.ID != uuid.Nil {
		return s.loginWithUser(
			ctx,
			authIdentity.UserID,
			metadata,
		)
	}

	user, err := s.repository.GetUserByEmail(
		ctx,
		identity.Email,
	)
	if err != nil {
		return LoginGoogleResult{}, err
	}

	if user.ID != uuid.Nil {
		expiresAt := time.Now().Add(
			AccountLinkConfirmationLifetime,
		)

		confirmation, err := s.repository.CreateAccountLinkConfirmation(
			ctx,
			logindb.CreateAccountLinkConfirmationParams{
				UserID:              user.ID,
				Provider:            "google",
				ProviderSubject:     identity.Subject,
				EmailSnapshot:       pgtype.Text{
					String: identity.Email,
					Valid:  true,
				},
				DisplayNameSnapshot: pgtype.Text{
					String: identity.DisplayName,
					Valid:  identity.DisplayName != "",
				},
				ExpiresAt: pgtype.Timestamptz{
					Time:  expiresAt,
					Valid: true,
				},
			},
		)
		if err != nil {
			return LoginGoogleResult{}, err
		}

		return LoginGoogleResult{
			Outcome:        LoginOutcomeAccountLinkRequired,
			ConfirmationID: confirmation.ID,
		}, nil
	}

	verificationID, err := s.registrationService.CreateSocialRegistration(
    	ctx,
    	registration.CreateSocialRegistrationInput{
        	Email:               identity.Email,
        	Provider:            "google",
        	ProviderSubject:     identity.Subject,
        	EmailSnapshot:       pgtype.Text{
            String: identity.Email,
            Valid:  true,
        	},
        	DisplayNameSnapshot: pgtype.Text{
            String: identity.DisplayName,
            Valid:  identity.DisplayName != "",
        	},
    	},
	)
	if err != nil {
    return LoginGoogleResult{}, err
	}

	return LoginGoogleResult{
    Outcome:        LoginOutcomeRegistrationRequired,
    VerificationID: verificationID,
	}, nil
}

type GetAccountLinkConfirmationResult struct {
	ID                  uuid.UUID
	Provider            string
	EmailSnapshot       string
	DisplayNameSnapshot string
	UserEmail           string
	UserDisplayName     string
}

func (s *Service) GetAccountLinkConfirmation(
	ctx context.Context,
	id uuid.UUID,
) (GetAccountLinkConfirmationResult, error) {
	confirmation, err := s.repository.GetActiveAccountLinkConfirmation(ctx, id)
	if err != nil {
		return GetAccountLinkConfirmationResult{}, err
	}

	return GetAccountLinkConfirmationResult{
		ID:                  confirmation.ID,
		Provider:            confirmation.Provider,
		EmailSnapshot:       confirmation.EmailSnapshot.String,
		DisplayNameSnapshot: confirmation.DisplayNameSnapshot.String,
		UserEmail:           confirmation.UserEmail,
		UserDisplayName:     confirmation.UserDisplayName,
	}, nil
}

func (s *Service) ConfirmAccountLink(
	ctx context.Context,
	confirmationID uuid.UUID,
	metadata session.SessionMetadata,
) (LoginGoogleResult, error) {
	identity, err := s.repository.ConfirmAccountLink(
		ctx,
		confirmationID,
	)
	if err != nil {
		return LoginGoogleResult{}, err
	}

	return s.loginWithUser(
		ctx,
		identity.UserID,
		metadata,
	)
}

func (s *Service) loginWithUser(
	ctx context.Context,
	userID uuid.UUID,
	metadata session.SessionMetadata,
) (LoginGoogleResult, error) {
	user, err := s.repository.GetUserForLoginByID(
		ctx,
		userID,
	)
	if err != nil {
		return LoginGoogleResult{}, err
	}

	sessionResult, err := s.sessionService.CreateSession(
		ctx,
		user.ID,
		metadata,
	)
	if err != nil {
		return LoginGoogleResult{}, err
	}

	accessToken, err := s.accessTokenGenerator.Generate(
		user.ID,
		sessionResult.Session.ID,
		session.AccessTokenLifetime,
	)
	if err != nil {
		return LoginGoogleResult{}, apperror.Internal(err)
	}

	return LoginGoogleResult{
		Outcome:      LoginOutcomeAuthenticated,
		User:         &user,
		Session:      &sessionResult.Session,
		AccessToken:  accessToken,
		RefreshToken: sessionResult.RefreshToken,
	}, nil
}