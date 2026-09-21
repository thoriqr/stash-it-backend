package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	sessionmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/session/mocks"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func TestService_RefreshToken(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		accessTokenGenerator := security.NewAccessTokenGenerator(
			[]byte("test-secret"),
		)

		svc := session.NewService(repo, accessTokenGenerator)

		userID := uuid.New()
		sessionID := uuid.New()
		refreshToken := "old-refresh-token"

		currentToken := sessiondb.GetRefreshTokenWithSessionForUpdateRow{
			UserID:    userID,
			SessionID: sessionID,
		}

		var capturedTokenHash string
		var capturedNewTokenHash string
		var capturedPolicy session.RefreshTokenPolicy

		repo.EXPECT().
			RefreshToken(
				gomock.Any(),
				gomock.Any(),
				gomock.Any(),
				gomock.Any(),
			).
			DoAndReturn(func(
				_ context.Context,
				tokenHash string,
				newRefreshTokenHash string,
				policy session.RefreshTokenPolicy,
			) (
				sessiondb.GetRefreshTokenWithSessionForUpdateRow,
				sessiondb.RefreshToken,
				error,
			) {
				capturedTokenHash = tokenHash
				capturedNewTokenHash = newRefreshTokenHash
				capturedPolicy = policy

				return currentToken, sessiondb.RefreshToken{}, nil
			})

		result, err := svc.RefreshToken(
			context.Background(),
			refreshToken,
		)

		require.NoError(t, err)

		require.NotEmpty(t, result.AccessToken)
		require.NotEmpty(t, result.RefreshToken)
		require.NotEqual(t, refreshToken, result.RefreshToken)

		require.Equal(
			t,
			security.HashToken(refreshToken),
			capturedTokenHash,
		)

		require.Equal(
			t,
			security.HashToken(result.RefreshToken),
			capturedNewTokenHash,
		)

		require.Equal(
			t,
			session.SessionIdleLifetime,
			capturedPolicy.IdleLifetime,
		)

		verifier := security.NewAccessTokenVerifier(
			[]byte("test-secret"),
		)

		claims, err := verifier.Verify(result.AccessToken)

		require.NoError(t, err)
		require.Equal(t, userID.String(), claims.Subject)
		require.Equal(t, sessionID, claims.SessionID)
	})

	t.Run("repository error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		accessTokenGenerator := security.NewAccessTokenGenerator(
			[]byte("test-secret"),
		)

		svc := session.NewService(repo, accessTokenGenerator)

		refreshToken := "old-refresh-token"
		expectedErr := errors.New("repository error")

		repo.EXPECT().
			RefreshToken(
				gomock.Any(),
				gomock.Any(),
				gomock.Any(),
				gomock.Any(),
			).
			Return(
				sessiondb.GetRefreshTokenWithSessionForUpdateRow{},
				sessiondb.RefreshToken{},
				expectedErr,
			)

		result, err := svc.RefreshToken(
			context.Background(),
			refreshToken,
		)

		require.ErrorIs(t, err, expectedErr)
		require.Zero(t, result)
	})
}