package auth

import (
	"context"

	"github.com/google/uuid"

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

func (r *Repository) CreateUser(ctx context.Context, email string) (authdb.User, error) {
	return r.queries.CreateUser(ctx, email)
}

func (r *Repository) GetUserByID(ctx context.Context, id uuid.UUID) (authdb.User, error) {
	return r.queries.GetUserByID(ctx, id)
}

func (r *Repository) GetUserByEmail(ctx context.Context, email string) (authdb.User, error) {
	return r.queries.GetUserByEmail(ctx, email)
}

func (r *Repository) ListUsers(ctx context.Context) ([]authdb.User, error) {
	return r.queries.ListUsers(ctx)
}