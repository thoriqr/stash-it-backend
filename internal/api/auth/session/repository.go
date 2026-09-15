package session

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

type Repository interface {
	CreateSession(
		ctx context.Context,
		params sessiondb.CreateSessionParams,
		refreshTokenHash string,
	) (sessiondb.Session, sessiondb.RefreshToken, error)
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