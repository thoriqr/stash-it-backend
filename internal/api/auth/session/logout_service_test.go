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
	t.Run("revokes the caller's own session", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		userID := uuid.New()
		sessionID := uuid.New()

		// Both identifiers are forwarded. The repository matches both, so a
		// session id belonging to another user is refused there rather than here.
		repo.EXPECT().
			RevokeSession(gomock.Any(), sessionID, userID).
			Return(nil)

		err := svc.Logout(context.Background(), userID, sessionID)

		require.NoError(t, err)
	})

	// Logout reports the caller's intent, not the outcome it aimed at. A session
	// that is already revoked, or that is not the caller's, revokes nothing and is
	// still a success, which is what makes logging out twice safe.
	t.Run("reports success when the session matched nothing", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		userID := uuid.New()
		foreignSessionID := uuid.New()

		repo.EXPECT().
			RevokeSession(gomock.Any(), foreignSessionID, userID).
			Return(nil)

		err := svc.Logout(context.Background(), userID, foreignSessionID)

		require.NoError(t, err)
	})

	t.Run("repository error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		userID := uuid.New()
		sessionID := uuid.New()
		expectedErr := errors.New("repository error")

		repo.EXPECT().
			RevokeSession(gomock.Any(), sessionID, userID).
			Return(expectedErr)

		err := svc.Logout(context.Background(), userID, sessionID)

		require.ErrorIs(t, err, expectedErr)
	})
}
