package auth

import (
	"context"

	authdb "github.com/thoriqr/stash-it-backend/internal/auth/generated"
)

type Service struct {
	repository UserRepository
}

func NewService(repository UserRepository) *Service {
	return &Service{
		repository: repository,
	}
}

func (s *Service) CreateUser(
	ctx context.Context, email string,
) (authdb.User, error) {
	return s.repository.CreateUser(ctx, email)
}