package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessionmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/session/mocks"
)

func TestService_RevokeSessionForUser(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		userID := uuid.New()
		sessionID := uuid.New()

		repo.EXPECT().
			RevokeSessionForUser(
				gomock.Any(),
				sessionID,
				userID,
			).
			Return(nil)

		err := svc.RevokeSessionForUser(
			context.Background(),
			userID,
			sessionID,
		)

		require.NoError(t, err)
	})

	t.Run("repository error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		userID := uuid.New()
		sessionID := uuid.New()
		expectedErr := errors.New("revoke session error")

		repo.EXPECT().
			RevokeSessionForUser(
				gomock.Any(),
				sessionID,
				userID,
			).
			Return(expectedErr)

		err := svc.RevokeSessionForUser(
			context.Background(),
			userID,
			sessionID,
		)

		require.ErrorIs(t, err, expectedErr)
	})
}