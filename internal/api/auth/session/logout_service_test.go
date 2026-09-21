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

func TestService_Logout(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		sessionID := uuid.New()

		repo.EXPECT().
			RevokeSession(gomock.Any(), sessionID).
			Return(nil)

		err := svc.Logout(context.Background(), sessionID)

		require.NoError(t, err)
	})

	t.Run("repository error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		sessionID := uuid.New()
		expectedErr := errors.New("repository error")

		repo.EXPECT().
			RevokeSession(gomock.Any(), sessionID).
			Return(expectedErr)

		err := svc.Logout(context.Background(), sessionID)

		require.ErrorIs(t, err, expectedErr)
	})
}