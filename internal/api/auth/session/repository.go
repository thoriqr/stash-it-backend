package session

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

type RefreshTokenPolicy struct {
	IdleLifetime time.Duration
}

type Repository interface {
	CreateSession(
		ctx context.Context,
		params sessiondb.CreateSessionParams,
		refreshTokenHash string,
	) (sessiondb.Session, sessiondb.RefreshToken, error)

	GetRefreshTokenWithSession(
		ctx context.Context,
		tokenHash string,
	) (sessiondb.GetRefreshTokenWithSessionRow, error)

	RefreshToken(
		ctx context.Context,
		tokenHash string,
		newRefreshTokenHash string,
		policy RefreshTokenPolicy,
	) (
		sessiondb.GetRefreshTokenWithSessionForUpdateRow,
		sessiondb.RefreshToken,
		error,
	)

	RevokeSession(
		ctx context.Context,
		sessionID uuid.UUID,
	) error

	ListSessions(
		ctx context.Context,
		userID uuid.UUID,
		offset int32,
		limit int32,
	) ([]sessiondb.Session, error)

	CountSessions(
		ctx context.Context,
		userID uuid.UUID,
	) (int64, error)

	RevokeSessionForUser(
		ctx context.Context,
		sessionID uuid.UUID,
		userID uuid.UUID,
	) error
}

type repository struct {
	queries *sessiondb.Queries
	pool    *pgxpool.Pool
}

func NewRepository(
	queries *sessiondb.Queries,
	pool *pgxpool.Pool,
) Repository {
	return &repository{
		queries: queries,
		pool:    pool,
	}
}

func (r *repository) CreateSession(
	ctx context.Context,
	params sessiondb.CreateSessionParams,
	refreshTokenHash string,
) (sessiondb.Session, sessiondb.RefreshToken, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return sessiondb.Session{}, sessiondb.RefreshToken{}, apperror.Internal(err)
	}
	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	session, err := qtx.CreateSession(ctx, params)
	if err != nil {
		return sessiondb.Session{}, sessiondb.RefreshToken{}, apperror.Internal(err)
	}

	refreshToken, err := qtx.CreateRefreshToken(ctx, sessiondb.CreateRefreshTokenParams{
		SessionID: session.ID,
		TokenHash: refreshTokenHash,
	})
	if err != nil {
		return sessiondb.Session{}, sessiondb.RefreshToken{}, apperror.Internal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return sessiondb.Session{}, sessiondb.RefreshToken{}, apperror.Internal(err)
	}

	return session, refreshToken, nil
}

func (r *repository) GetRefreshTokenWithSession(
	ctx context.Context,
	tokenHash string,
) (sessiondb.GetRefreshTokenWithSessionRow, error) {
	record, err := r.queries.GetRefreshTokenWithSession(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sessiondb.GetRefreshTokenWithSessionRow{}, apperror.UnauthorizedWith(
				CodeRefreshTokenInvalid,
				"invalid refresh token",
				err,
			)
		}

		return sessiondb.GetRefreshTokenWithSessionRow{}, apperror.Internal(err)
	}

	return record, nil
}

func (r *repository) RefreshToken(
	ctx context.Context,
	tokenHash string,
	newRefreshTokenHash string,
	policy RefreshTokenPolicy,
) (
	sessiondb.GetRefreshTokenWithSessionForUpdateRow,
	sessiondb.RefreshToken,
	error,
) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.Internal(err)
	}
	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	currentToken, err := qtx.GetRefreshTokenWithSessionForUpdate(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.UnauthorizedWith(
				CodeRefreshTokenInvalid,
				"invalid refresh token",
				err,
			)
		}

		return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.Internal(err)
	}

	if currentToken.RevokedAt.Valid {
		return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.UnauthorizedWith(
			CodeSessionRevoked,
			"session has been revoked",
			nil,
		)
	}

	if currentToken.ReplacedBy.Valid {
		if err := qtx.RevokeSession(ctx, currentToken.SessionID); err != nil {
			return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.Internal(err)
		}

		if err := tx.Commit(ctx); err != nil {
			return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.Internal(err)
		}

		return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.UnauthorizedWith(
			CodeSessionRevoked,
			"session has been revoked",
			nil,
		)
	}

	now := time.Now()

	if now.After(currentToken.AbsoluteExpiresAt.Time) {
		if err := qtx.RevokeSession(ctx, currentToken.SessionID); err != nil {
			return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.Internal(err)
		}

		if err := tx.Commit(ctx); err != nil {
			return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.Internal(err)
		}

		return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.UnauthorizedWith(
			CodeSessionExpired,
			"session has expired",
			nil,
		)
	}

	if now.Sub(currentToken.LastActivityAt.Time) > policy.IdleLifetime {
		if err := qtx.RevokeSession(ctx, currentToken.SessionID); err != nil {
			return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.Internal(err)
		}

		if err := tx.Commit(ctx); err != nil {
			return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.Internal(err)
		}

		return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.UnauthorizedWith(
			CodeSessionExpired,
			"session has expired",
			nil,
		)
	}

	newToken, err := qtx.CreateRefreshToken(ctx, sessiondb.CreateRefreshTokenParams{
		SessionID: currentToken.SessionID,
		TokenHash: newRefreshTokenHash,
	})
	if err != nil {
		return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.Internal(err)
	}

	if err := qtx.ReplaceRefreshToken(ctx, sessiondb.ReplaceRefreshTokenParams{
		ReplacedBy: pgtype.UUID{
			Bytes: newToken.ID,
			Valid: true,
		},
		ID: currentToken.ID,
	}); err != nil {
		return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.Internal(err)
	}

	if err := qtx.UpdateSessionActivity(ctx, currentToken.SessionID); err != nil {
		return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.Internal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return sessiondb.GetRefreshTokenWithSessionForUpdateRow{}, sessiondb.RefreshToken{}, apperror.Internal(err)
	}

	return currentToken, newToken, nil
}

func (r *repository) RevokeSession(
	ctx context.Context,
	sessionID uuid.UUID,
) error {
	if err := r.queries.RevokeSession(ctx, sessionID); err != nil {
		return apperror.Internal(err)
	}

	return nil
}

func (r *repository) ListSessions(
	ctx context.Context,
	userID uuid.UUID,
	offset int32,
	limit int32,
) ([]sessiondb.Session, error) {
	sessions, err := r.queries.ListSessions(
		ctx,
		sessiondb.ListSessionsParams{
			UserID:     userID,
			PageOffset: offset,
			PageLimit:  limit,
		},
	)
	if err != nil {
		return nil, apperror.Internal(err)
	}

	return sessions, nil
}

func (r *repository) CountSessions(
	ctx context.Context,
	userID uuid.UUID,
) (int64, error) {
	count, err := r.queries.CountSessions(ctx, userID)
	if err != nil {
		return 0, apperror.Internal(err)
	}

	return count, nil
}

func (r *repository) RevokeSessionForUser(
	ctx context.Context,
	sessionID uuid.UUID,
	userID uuid.UUID,
) error {
	_, err := r.queries.RevokeSessionForUser(
		ctx,
		sessiondb.RevokeSessionForUserParams{
			SessionID: sessionID,
			UserID: userID,
		},
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperror.NotFound(err)
		}

		return apperror.Internal(err)
	}

	return nil
}