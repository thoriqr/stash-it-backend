package login

import (
	"context"

	logindb "github.com/thoriqr/stash-it-backend/internal/api/auth/login/generated"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

type Service struct {
	repository           Repository
	sessionService       session.SessionCreator
	passwordHasher       *security.PasswordHasher
	accessTokenGenerator *security.AccessTokenGenerator
}

func NewService(
	repository Repository,
	sessionService session.SessionCreator,
	passwordHasher *security.PasswordHasher,
	accessTokenGenerator *security.AccessTokenGenerator,
) *Service {
	return &Service{
		repository:           repository,
		sessionService:       sessionService,
		passwordHasher:       passwordHasher,
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