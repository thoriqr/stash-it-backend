package login

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	logindb "github.com/thoriqr/stash-it-backend/internal/api/auth/login/generated"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

type LoginSessionMetadata struct {
	Platform       string
	InstallationID pgtype.UUID
	DeviceName     pgtype.Text
	UserAgent      pgtype.Text
}

type Service struct {
	repository         Repository
	sessionRepository  session.Repository
	passwordHasher     *security.PasswordHasher
	accessTokenGenerator *security.AccessTokenGenerator
}

func NewService(
	repository Repository,
	sessionRepository session.Repository,
	passwordHasher *security.PasswordHasher,
	accessTokenGenerator *security.AccessTokenGenerator,
) *Service {
	return &Service{
		repository:          repository,
		sessionRepository:   sessionRepository,
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
	metadata LoginSessionMetadata,
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

	refreshToken, err := security.GenerateToken()
	if err != nil {
		return LoginResult{}, apperror.Internal(err)
	}

	refreshTokenHash := security.HashToken(refreshToken)

	sessionRecord, _, err := s.sessionRepository.CreateSession(
		ctx,
		sessiondb.CreateSessionParams{
			UserID:          user.ID,
			Platform:        metadata.Platform,
			InstallationID:  metadata.InstallationID,
			DeviceName:      metadata.DeviceName,
			UserAgent:       metadata.UserAgent,
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  time.Now().Add(sessionAbsoluteLifetime),
				Valid: true,
			},
		},
		refreshTokenHash,
	)
	if err != nil {
		return LoginResult{}, err
	}

	accessToken, err := s.accessTokenGenerator.Generate(
		user.ID,
		sessionRecord.ID,
		accessTokenLifetime,
	)
	if err != nil {
		return LoginResult{}, apperror.Internal(err)
	}

	return LoginResult{
		User:         user,
		Session:      sessionRecord,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
	}, nil
}