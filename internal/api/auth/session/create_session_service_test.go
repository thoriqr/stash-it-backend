package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	sessionmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/session/mocks"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func TestService_CreateSession(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		userID := uuid.New()
		sessionID := uuid.New()
		refreshTokenID := uuid.New()
		metadata := session.SessionMetadata{}

		expectedSession := sessiondb.Session{
			ID:     sessionID,
			UserID: userID,
		}
		expectedRefreshToken := sessiondb.RefreshToken{
			ID:        refreshTokenID,
			SessionID: sessionID,
		}

		var capturedParams sessiondb.CreateSessionParams
		var capturedHash string

		repo.EXPECT().
			CreateSession(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(
				_ context.Context,
				params sessiondb.CreateSessionParams,
				refreshTokenHash string,
			) (sessiondb.Session, sessiondb.RefreshToken, error) {
				capturedParams = params
				capturedHash = refreshTokenHash

				return expectedSession, expectedRefreshToken, nil
			})

		before := time.Now()

		result, err := svc.CreateSession(context.Background(), userID, metadata)

		after := time.Now()

		require.NoError(t, err)
		require.Equal(t, expectedSession, result.Session)

		require.Equal(t, userID, capturedParams.UserID)
		require.Equal(t, metadata.Platform, capturedParams.Platform)
		require.Equal(t, metadata.InstallationID, capturedParams.InstallationID)
		require.Equal(t, metadata.DeviceName, capturedParams.DeviceName)
		require.Equal(t, metadata.UserAgent, capturedParams.UserAgent)

		require.NotEmpty(t, result.RefreshToken)
		require.NotEmpty(t, capturedHash)
		require.Equal(
			t,
			security.HashToken(result.RefreshToken),
			capturedHash,
		)

		expectedMin := before.Add(session.SessionAbsoluteLifetime)
		expectedMax := after.Add(session.SessionAbsoluteLifetime)

		require.GreaterOrEqual(t, capturedParams.AbsoluteExpiresAt.Time, expectedMin)
		require.LessOrEqual(t, capturedParams.AbsoluteExpiresAt.Time, expectedMax)
	})

	t.Run("repository error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		userID := uuid.New()
		metadata := session.SessionMetadata{}
		expectedErr := errors.New("repository error")

		repo.EXPECT().
			CreateSession(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(sessiondb.Session{}, sessiondb.RefreshToken{}, expectedErr)

		result, err := svc.CreateSession(context.Background(), userID, metadata)

		require.ErrorIs(t, err, expectedErr)
		require.Zero(t, result)
	})
}