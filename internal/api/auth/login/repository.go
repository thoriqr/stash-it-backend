package login

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	logindb "github.com/thoriqr/stash-it-backend/internal/api/auth/login/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

type Repository interface {
	GetUserForLogin(
		ctx context.Context,
		email string,
	) (logindb.GetUserForLoginRow, error)
}

type repository struct {
	queries *logindb.Queries
}

func NewRepository(queries *logindb.Queries) Repository {
	return &repository{
		queries: queries,
	}
}

func (r *repository) GetUserForLogin(
	ctx context.Context,
	email string,
) (logindb.GetUserForLoginRow, error) {
	user, err := r.queries.GetUserForLogin(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return logindb.GetUserForLoginRow{}, apperror.UnauthorizedWith(
				CodeInvalidCredentials,
				"invalid email or password",
				err,
			)
		}

		return logindb.GetUserForLoginRow{}, apperror.Internal(err)
	}

	return user, nil
}