package login

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

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

    GetActiveAccountLinkConfirmation(
        ctx context.Context,
        id uuid.UUID,
    ) (logindb.GetActiveAccountLinkConfirmationRow, error)

    CreateAuthIdentity(
        ctx context.Context,
        params logindb.CreateAuthIdentityParams,
    ) (logindb.CreateAuthIdentityRow, error)

    CreateAccountLinkConfirmation(
        ctx context.Context,
        params logindb.CreateAccountLinkConfirmationParams,
    ) (logindb.AccountLinkConfirmation, error)

		ConfirmAccountLink(
			ctx context.Context,
			confirmationID uuid.UUID,
		) (logindb.CreateAuthIdentityRow, error)
}

type repository struct {
	pool    *pgxpool.Pool
	queries *logindb.Queries
}

func NewRepository(
	pool *pgxpool.Pool,
	queries *logindb.Queries,
) Repository {
	return &repository{
		pool:    pool,
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

func (r *repository) GetActiveAccountLinkConfirmation(
    ctx context.Context,
    id uuid.UUID,
) (logindb.GetActiveAccountLinkConfirmationRow, error) {
    confirmation, err := r.queries.GetActiveAccountLinkConfirmation(ctx, id)
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return logindb.GetActiveAccountLinkConfirmationRow{},
                apperror.ConflictWith(
                    CodeAccountLinkConfirmationInvalid,
                    "account link confirmation is invalid",
                    err,
                )
        }

        return logindb.GetActiveAccountLinkConfirmationRow{}, apperror.Internal(err)
    }

    return confirmation, nil
}

func (r *repository) CreateAuthIdentity(
	ctx context.Context,
	params logindb.CreateAuthIdentityParams,
) (logindb.CreateAuthIdentityRow, error) {
	identity, err := r.queries.CreateAuthIdentity(
		ctx,
		params,
	)
	if err != nil {
		return logindb.CreateAuthIdentityRow{}, mapLoginDBError(err)
	}

	return identity, nil
}

func (r *repository) CreateAccountLinkConfirmation(
    ctx context.Context,
    params logindb.CreateAccountLinkConfirmationParams,
) (logindb.AccountLinkConfirmation, error) {
    confirmation, err := r.queries.CreateAccountLinkConfirmation(
        ctx,
        params,
    )
    if err != nil {
        return logindb.AccountLinkConfirmation{}, apperror.Internal(err)
    }

    return confirmation, nil
}

func (r *repository) ConfirmAccountLink(
	ctx context.Context,
	confirmationID uuid.UUID,
) (logindb.CreateAuthIdentityRow, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return logindb.CreateAuthIdentityRow{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	confirmation, err := qtx.GetAccountLinkConfirmationForUpdate(
		ctx,
		confirmationID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return logindb.CreateAuthIdentityRow{}, apperror.ConflictWith(
				CodeAccountLinkConfirmationInvalid,
				"account link confirmation is invalid",
				err,
			)
		}

		return logindb.CreateAuthIdentityRow{}, apperror.Internal(err)
	}

	identity, err := qtx.CreateAuthIdentity(
		ctx,
		logindb.CreateAuthIdentityParams{
			UserID:              confirmation.UserID,
			Provider:            confirmation.Provider,
			ProviderSubject:     confirmation.ProviderSubject,
			EmailSnapshot:       confirmation.EmailSnapshot,
			DisplayNameSnapshot: confirmation.DisplayNameSnapshot,
		},
	)
	if err != nil {
		return logindb.CreateAuthIdentityRow{}, mapLoginDBError(err)
	}

	rowsAffected, err := qtx.MarkAccountLinkConfirmationConfirmed(
		ctx,
		confirmationID,
	)
	if err != nil {
		return logindb.CreateAuthIdentityRow{}, apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return logindb.CreateAuthIdentityRow{}, apperror.ConflictWith(
			CodeAccountLinkConfirmationInvalid,
			"account link confirmation is invalid",
			nil,
		)
	}

	if err := tx.Commit(ctx); err != nil {
		return logindb.CreateAuthIdentityRow{}, apperror.Internal(err)
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