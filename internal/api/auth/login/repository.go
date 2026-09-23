package login

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	logindb "github.com/thoriqr/stash-it-backend/internal/api/auth/login/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

type Repository interface {
	GetUserForLogin(
		ctx context.Context,
		email string,
	) (logindb.GetUserForLoginRow, error)

	GetUserForLoginByID(
    ctx context.Context,
    userID uuid.UUID,
) (logindb.GetUserForLoginByIDRow, error)

	GetAuthIdentity(
		ctx context.Context,
		provider string,
		providerSubject string,
	) (logindb.GetAuthIdentityRow, error)

	GetUserByEmail(
		ctx context.Context,
		email string,
	) (logindb.GetUserByEmailRow, error)

	CreateAuthIdentity(
		ctx context.Context,
		params logindb.CreateAuthIdentityParams,
	) (logindb.AuthIdentity, error)
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

func (r *repository) GetUserForLoginByID(
    ctx context.Context,
    userID uuid.UUID,
) (logindb.GetUserForLoginByIDRow, error) {
    user, err := r.queries.GetUserForLoginByID(
        ctx,
        userID,
    )
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return logindb.GetUserForLoginByIDRow{}, apperror.NotFound(err)
        }

        return logindb.GetUserForLoginByIDRow{}, apperror.Internal(err)
    }

    return user, nil
}

func (r *repository) GetAuthIdentity(
	ctx context.Context,
	provider string,
	providerSubject string,
) (logindb.GetAuthIdentityRow, error) {
	identity, err := r.queries.GetAuthIdentity(
		ctx,
		logindb.GetAuthIdentityParams{
			Provider:        provider,
			ProviderSubject: providerSubject,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return logindb.GetAuthIdentityRow{}, nil
		}

		return logindb.GetAuthIdentityRow{}, apperror.Internal(err)
	}

	return identity, nil
}

func (r *repository) GetUserByEmail(
	ctx context.Context,
	email string,
) (logindb.GetUserByEmailRow, error) {
	user, err := r.queries.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return logindb.GetUserByEmailRow{}, nil
		}

		return logindb.GetUserByEmailRow{}, apperror.Internal(err)
	}

	return user, nil
}

func (r *repository) CreateAuthIdentity(
	ctx context.Context,
	params logindb.CreateAuthIdentityParams,
) (logindb.AuthIdentity, error) {
	identity, err := r.queries.CreateAuthIdentity(
		ctx,
		params,
	)
	if err != nil {
		return logindb.AuthIdentity{}, mapLoginDBError(err)
	}

	return identity, nil
}

func mapLoginDBError(err error) error {
	var pgErr *pgconn.PgError

	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "auth_identities_provider_subject_unique":
			return apperror.ConflictWith(
				CodeAuthIdentityAlreadyExists,
				"auth identity already exists",
				err,
			)
		}
	}

	return apperror.Internal(err)
}