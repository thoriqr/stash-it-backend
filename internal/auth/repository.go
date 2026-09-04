package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	authdb "github.com/thoriqr/stash-it-backend/internal/auth/generated"
)

type UserRepository interface {
	CreateUser(ctx context.Context, email string) (authdb.User, error)
	GetUserByID(ctx context.Context, id uuid.UUID) (authdb.User, error)
	GetUserByEmail(ctx context.Context, email string) (authdb.User, error)
	ListUsers(ctx context.Context) ([]authdb.User, error)
}

type Repository struct {
	queries *authdb.Queries
}

func NewRepository(queries *authdb.Queries) *Repository {
	return &Repository{
		queries: queries,
	}
}

func (r *Repository) CreateUser(
	ctx context.Context,
	email string,
) (authdb.User, error) {
	user, err := r.queries.CreateUser(ctx, email)
	if err != nil {
		var pgErr *pgconn.PgError

		if errors.As(err, &pgErr) &&
			pgErr.ConstraintName == "users_email_unique" {
			return authdb.User{}, apperror.ConflictWith(
				"EMAIL_ALREADY_REGISTERED",
				"email is already registered",
				err,
			)
		}

		return authdb.User{}, apperror.Internal(err)
	}

	return user, nil
}

func (r *Repository) GetUserByID(
	ctx context.Context,
	id uuid.UUID,
) (authdb.User, error) {
	user, err := r.queries.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return authdb.User{}, apperror.NotFoundWith(
				"USER_NOT_FOUND",
				"user not found",
				err,
			)
		}

		return authdb.User{}, apperror.Internal(err)
	}

	return user, nil
}

func (r *Repository) GetUserByEmail(
	ctx context.Context,
	email string,
) (authdb.User, error) {
	user, err := r.queries.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return authdb.User{}, apperror.NotFoundWith(
				"USER_NOT_FOUND",
				"user not found",
				err,
			)
		}

		return authdb.User{}, apperror.Internal(err)
	}

	return user, nil
}

func (r *Repository) ListUsers(
	ctx context.Context,
) ([]authdb.User, error) {
	users, err := r.queries.ListUsers(ctx)
	if err != nil {
		return nil, apperror.Internal(err)
	}

	return users, nil
}