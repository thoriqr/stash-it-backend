package session

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

type SessionCreator interface {
	CreateSession(
		ctx context.Context,
		userID uuid.UUID,
		metadata SessionMetadata,
	) (CreateSessionResult, error)
}

type SessionService interface {
    RefreshToken(
        ctx context.Context,
        refreshToken string,
    ) (RefreshTokenResult, error)

    Logout(
        ctx context.Context,
        sessionID uuid.UUID,
    ) error

    ListSessions(
        ctx context.Context,
        userID uuid.UUID,
        page int,
        limit int,
    ) (ListSessionsResult, error)

		RevokeSessionForUser(
			ctx context.Context,
			userID uuid.UUID,
			sessionID uuid.UUID,
		) error
}

type service struct {
	repository           Repository
	accessTokenGenerator *security.AccessTokenGenerator
}

func NewService(
	repository Repository,
	accessTokenGenerator *security.AccessTokenGenerator,
) *service {
	return &service{
		repository:           repository,
		accessTokenGenerator: accessTokenGenerator,
	}
}

type CreateSessionResult struct {
	Session      sessiondb.Session
	RefreshToken string
}

func (s *service) CreateSession(
	ctx context.Context,
	userID uuid.UUID,
	metadata SessionMetadata,
) (CreateSessionResult, error) {
	refreshToken, err := security.GenerateToken()
	if err != nil {
		return CreateSessionResult{}, apperror.Internal(err)
	}

	refreshTokenHash := security.HashToken(refreshToken)

	sessionRecord, _, err := s.repository.CreateSession(
		ctx,
		sessiondb.CreateSessionParams{
			UserID:           userID,
			Platform:         metadata.Platform,
			InstallationID:   metadata.InstallationID,
			DeviceName:       metadata.DeviceName,
			UserAgent:        metadata.UserAgent,
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  time.Now().Add(SessionAbsoluteLifetime),
				Valid: true,
			},
		},
		refreshTokenHash,
	)
	if err != nil {
		return CreateSessionResult{}, err
	}

	return CreateSessionResult{
		Session:      sessionRecord,
		RefreshToken: refreshToken,
	}, nil
}

type RefreshTokenResult struct {
	AccessToken  string
	RefreshToken string
}

func (s *service) RefreshToken(
	ctx context.Context,
	refreshToken string,
) (RefreshTokenResult, error) {
	newRefreshToken, err := security.GenerateToken()
	if err != nil {
		return RefreshTokenResult{}, apperror.Internal(err)
	}

	newRefreshTokenHash := security.HashToken(newRefreshToken)
	refreshTokenHash := security.HashToken(refreshToken)

	currentToken, _, err := s.repository.RefreshToken(
		ctx,
		refreshTokenHash,
		newRefreshTokenHash,
		RefreshTokenPolicy{
			IdleLifetime: SessionIdleLifetime,
		},
	)
	if err != nil {
		return RefreshTokenResult{}, err
	}

	accessToken, err := s.accessTokenGenerator.Generate(
		currentToken.UserID,
		currentToken.SessionID,
		AccessTokenLifetime,
	)
	if err != nil {
		return RefreshTokenResult{}, apperror.Internal(err)
	}

	return RefreshTokenResult{
		AccessToken:  accessToken,
		RefreshToken: newRefreshToken,
	}, nil
}

func (s *service) Logout(
	ctx context.Context,
	sessionID uuid.UUID,
) error {
	return s.repository.RevokeSession(ctx, sessionID)
}

type ListSessionsResult struct {
	Sessions   []sessiondb.Session
	Page       int
	Limit      int
	Total      int64
	TotalPages int
}

func (s *service) ListSessions(
	ctx context.Context,
	userID uuid.UUID,
	page int,
	limit int,
) (ListSessionsResult, error) {
	if page < 1 {
		page = 1
	}

	if limit <= 0 {
		limit = SessionListDefaultLimit
	}

	if limit > SessionListMaxLimit {
		limit = SessionListMaxLimit
	}

	offset := int32((page - 1) * limit)

	sessions, err := s.repository.ListSessions(
		ctx,
		userID,
		offset,
		int32(limit),
	)
	if err != nil {
		return ListSessionsResult{}, err
	}

	total, err := s.repository.CountSessions(ctx, userID)
	if err != nil {
		return ListSessionsResult{}, err
	}

	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(limit) - 1) / int64(limit))
	}

	return ListSessionsResult{
		Sessions:   sessions,
		Page:       page,
		Limit:      limit,
		Total:      total,
		TotalPages: totalPages,
	}, nil
}

func (s *service) RevokeSessionForUser(
	ctx context.Context,
	userID uuid.UUID,
	sessionID uuid.UUID,
) error {
		return s.repository.RevokeSessionForUser(
			ctx,
			sessionID,
			userID,
		)
}