package user

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	userdb "github.com/thoriqr/stash-it-backend/internal/user/generated"
)

type UserRepository interface {
	GetUserByID(
		ctx context.Context,
		id uuid.UUID,
	) (userdb.GetUserByIDRow, error)

	GetUserByEmail(
		ctx context.Context,
		email string,
	) (userdb.GetUserByEmailRow, error)
}

type Repository struct {
	queries *userdb.Queries
}

func NewRepository(
	queries *userdb.Queries,
) *Repository {
	return &Repository{
		queries: queries,
	}
}

func (r *Repository) GetUserByID(
	ctx context.Context,
	id uuid.UUID,
) (userdb.GetUserByIDRow, error) {
	user, err := r.queries.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return userdb.GetUserByIDRow{}, apperror.NotFoundWith(
				"USER_NOT_FOUND",
				"user not found",
				err,
			)
		}

		return userdb.GetUserByIDRow{}, apperror.Internal(err)
	}

	return user, nil
}

func (r *Repository) GetUserByEmail(
	ctx context.Context,
	email string,
) (userdb.GetUserByEmailRow, error) {
	user, err := r.queries.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return userdb.GetUserByEmailRow{}, apperror.NotFoundWith(
				"USER_NOT_FOUND",
				"user not found",
				err,
			)
		}

		return userdb.GetUserByEmailRow{}, apperror.Internal(err)
	}

	return user, nil
}