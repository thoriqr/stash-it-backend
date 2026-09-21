package integration_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	session "github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	sessiondbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/session/generated"
)

func TestSession_CreateSession(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "session@test.com",
		DisplayName: "Session Test User",
	})
	require.NoError(t, err)

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	absoluteExpiresAt := time.Now().Add(24 * time.Hour)
	refreshTokenHash := "test-refresh-token-hash"

	createdSession, createdRefreshToken, err := repository.CreateSession(
		ctx,
		sessiondb.CreateSessionParams{
			UserID:  userID,
			Platform: "web",
			InstallationID: pgtype.UUID{
				Bytes:  [16]byte{1, 2, 3},
				Valid: true,
			},
			DeviceName: pgtype.Text{
				String: "Chrome",
				Valid:  true,
			},
			UserAgent: pgtype.Text{
				String: "Mozilla/5.0",
				Valid:  true,
			},
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  absoluteExpiresAt,
				Valid: true,
			},
		},
		refreshTokenHash,
	)
	require.NoError(t, err)

	assert.NotEqual(t, userID, createdSession.ID)
	assert.Equal(t, userID, createdSession.UserID)
	assert.Equal(t, "web", createdSession.Platform)

	assert.Equal(t, refreshTokenHash, createdRefreshToken.TokenHash)
	assert.Equal(t, createdSession.ID, createdRefreshToken.SessionID)

	assert.True(t, createdSession.InstallationID.Valid)
	assert.Equal(
		t,
		[16]byte{1, 2, 3},
		createdSession.InstallationID.Bytes,
	)

	assert.True(t, createdSession.DeviceName.Valid)
	assert.Equal(t, "Chrome", createdSession.DeviceName.String)

	assert.True(t, createdSession.UserAgent.Valid)
	assert.Equal(t, "Mozilla/5.0", createdSession.UserAgent.String)

	assert.True(t, createdSession.CreatedAt.Valid)
	assert.True(t, createdSession.LastActivityAt.Valid)
	assert.True(t, createdSession.AbsoluteExpiresAt.Valid)
	assert.WithinDuration(
		t,
		absoluteExpiresAt,
		createdSession.AbsoluteExpiresAt.Time,
		time.Second,
	)

	assert.False(t, createdSession.RevokedAt.Valid)
	assert.True(t, createdRefreshToken.IssuedAt.Valid)
	assert.False(t, createdRefreshToken.ReplacedBy.Valid)
}

func TestSession_GetRefreshTokenWithSession(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "refresh@test.com",
		DisplayName: "Refresh Test User",
	})
	require.NoError(t, err)

	sessionRecord, err := db.CreateTestSession(ctx, sessiondbtest.CreateTestSessionParams{
		UserID:  userID,
		Platform: "web",
		AbsoluteExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(24 * time.Hour),
			Valid: true,
		},
	})
	require.NoError(t, err)

	tokenHash := "test-refresh-token-hash"

	refreshToken, err := db.CreateTestRefreshToken(
		ctx,
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID: sessionRecord.ID,
			TokenHash: tokenHash,
			ReplacedBy: pgtype.UUID{},
		},
	)
	require.NoError(t, err)

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	result, err := repository.GetRefreshTokenWithSession(
		ctx,
		tokenHash,
	)
	require.NoError(t, err)

	assert.Equal(t, refreshToken.ID, result.ID)
	assert.Equal(t, refreshToken.SessionID, result.SessionID)
	assert.Equal(t, refreshToken.TokenHash, result.TokenHash)

	assert.Equal(t, userID, result.UserID)
	assert.Equal(t, sessionRecord.Platform, result.Platform)

	assert.Equal(
		t,
		sessionRecord.AbsoluteExpiresAt.Time,
		result.AbsoluteExpiresAt.Time,
	)

	assert.False(t, result.RevokedAt.Valid)
}

func TestSession_GetRefreshTokenWithSession_InvalidToken(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	_, err := repository.GetRefreshTokenWithSession(
		ctx,
		"non-existent-token-hash",
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)

	assert.Equal(t, http.StatusUnauthorized, appErr.Status)
	assert.Equal(t, session.CodeRefreshTokenInvalid, appErr.Code)
}

func TestSession_RefreshToken(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "rotate@test.com",
		DisplayName: "Rotate Test User",
	})
	require.NoError(t, err)

	sessionRecord, err := db.CreateTestSession(ctx, sessiondbtest.CreateTestSessionParams{
		UserID:  userID,
		Platform: "web",
		AbsoluteExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(24 * time.Hour),
			Valid: true,
		},
	})
	require.NoError(t, err)

	oldHash := "old-refresh-token-hash"
	newHash := "new-refresh-token-hash"

	oldToken, err := db.CreateTestRefreshToken(
		ctx,
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID: sessionRecord.ID,
			TokenHash: oldHash,
			ReplacedBy: pgtype.UUID{},
		},
	)
	require.NoError(t, err)

	// Save the session state before refresh.
	oldLastActivityAt := sessionRecord.LastActivityAt.Time

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	currentToken, newToken, err := repository.RefreshToken(
		ctx,
		oldHash,
		newHash,
		session.RefreshTokenPolicy{
			IdleLifetime: 30 * 24 * time.Hour,
		},
	)
	require.NoError(t, err)

	// Verify the token returned as the current token.
	assert.Equal(t, oldToken.ID, currentToken.ID)
	assert.Equal(t, sessionRecord.ID, currentToken.SessionID)
	assert.Equal(t, userID, currentToken.UserID)

	// Verify the new refresh token.
	assert.NotEqual(t, oldToken.ID, newToken.ID)
	assert.Equal(t, sessionRecord.ID, newToken.SessionID)
	assert.Equal(t, newHash, newToken.TokenHash)
	assert.True(t, newToken.IssuedAt.Valid)

	// Verify the old token state after the transaction committed.
	updatedOldToken, err := db.GetRefreshTokenState(ctx, oldToken.ID)
	require.NoError(t, err)

	assert.True(t, updatedOldToken.ReplacedBy.Valid)
	assert.Equal(
		t,
		newToken.ID,
		uuid.UUID(updatedOldToken.ReplacedBy.Bytes),
	)

	// Verify the session activity was updated.
	updatedSession, err := db.GetSessionState(ctx, sessionRecord.ID)
	require.NoError(t, err)

	assert.True(t, updatedSession.LastActivityAt.Valid)
	assert.True(
		t,
		updatedSession.LastActivityAt.Time.After(oldLastActivityAt),
	)
}

func TestSession_RefreshToken_Revoked(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "revoked@test.com",
		DisplayName: "Revoked Test User",
	})
	require.NoError(t, err)

	sessionRecord, err := db.CreateTestSession(
		ctx,
		sessiondbtest.CreateTestSessionParams{
			UserID:  userID,
			Platform: "web",
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  time.Now().Add(24 * time.Hour),
				Valid: true,
			},
			RevokedAt: pgtype.Timestamptz{
				Time:  time.Now(),
				Valid: true,
			},
		},
	)
	require.NoError(t, err)

	oldHash := "revoked-refresh-token-hash"

	oldToken, err := db.CreateTestRefreshToken(
		ctx,
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID: sessionRecord.ID,
			TokenHash: oldHash,
			ReplacedBy: pgtype.UUID{},
		},
	)
	require.NoError(t, err)

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	_, _, err = repository.RefreshToken(
		ctx,
		oldHash,
		"new-refresh-token-hash",
		session.RefreshTokenPolicy{
			IdleLifetime: 30 * 24 * time.Hour,
		},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)

	assert.Equal(t, http.StatusUnauthorized, appErr.Status)
	assert.Equal(t, session.CodeSessionRevoked, appErr.Code)

	// Make sure no new refresh token was created.
	_, err = db.GetRefreshTokenState(ctx, oldToken.ID)
	require.NoError(t, err)
}

func TestSession_RefreshToken_Replaced(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "replaced@test.com",
		DisplayName: "Replaced Test User",
	})
	require.NoError(t, err)

	sessionRecord, err := db.CreateTestSession(
		ctx,
		sessiondbtest.CreateTestSessionParams{
			UserID:  userID,
			Platform: "web",
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  time.Now().Add(24 * time.Hour),
				Valid: true,
			},
			RevokedAt: pgtype.Timestamptz{},
		},
	)
	require.NoError(t, err)

	oldHash := "old-replaced-token-hash"
	replacedHash := "already-replaced-token-hash"

	replacedToken, err := db.CreateTestRefreshToken(
		ctx,
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID:  sessionRecord.ID,
			TokenHash:  replacedHash,
			ReplacedBy: pgtype.UUID{},
		},
	)
	require.NoError(t, err)

	oldToken, err := db.CreateTestRefreshToken(
		ctx,
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID: sessionRecord.ID,
			TokenHash: oldHash,
			ReplacedBy: pgtype.UUID{
				Bytes:  replacedToken.ID,
				Valid: true,
			},
		},
	)
	require.NoError(t, err)

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	_, _, err = repository.RefreshToken(
		ctx,
		oldHash,
		"new-refresh-token-hash",
		session.RefreshTokenPolicy{
			IdleLifetime: 30 * 24 * time.Hour,
		},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)

	assert.Equal(t, http.StatusUnauthorized, appErr.Status)
	assert.Equal(t, session.CodeSessionRevoked, appErr.Code)

	currentOldToken, err := db.GetRefreshTokenState(ctx, oldToken.ID)
	require.NoError(t, err)

	assert.True(t, currentOldToken.ReplacedBy.Valid)
	assert.Equal(
		t,
		replacedToken.ID,
		uuid.UUID(currentOldToken.ReplacedBy.Bytes),
	)
}

func TestSession_RefreshToken_AbsoluteExpired(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "absolute-expired@test.com",
		DisplayName: "Absolute Expired Test User",
	})
	require.NoError(t, err)

	sessionRecord, err := db.CreateTestSession(
		ctx,
		sessiondbtest.CreateTestSessionParams{
			UserID:  userID,
			Platform: "web",
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  time.Now().Add(-time.Hour),
				Valid: true,
			},
			RevokedAt: pgtype.Timestamptz{},
		},
	)
	require.NoError(t, err)

	oldHash := "absolute-expired-refresh-token-hash"

	oldToken, err := db.CreateTestRefreshToken(
		ctx,
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID:  sessionRecord.ID,
			TokenHash:  oldHash,
			ReplacedBy: pgtype.UUID{},
		},
	)
	require.NoError(t, err)

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	_, _, err = repository.RefreshToken(
		ctx,
		oldHash,
		"new-refresh-token-hash",
		session.RefreshTokenPolicy{
			IdleLifetime: 30 * 24 * time.Hour,
		},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)

	assert.Equal(t, http.StatusUnauthorized, appErr.Status)
	assert.Equal(t, session.CodeSessionExpired, appErr.Code)

	// Verify the session was revoked.
	updatedSession, err := db.GetSessionState(ctx, sessionRecord.ID)
	require.NoError(t, err)

	assert.True(t, updatedSession.RevokedAt.Valid)

	// Verify the old refresh token was not replaced.
	updatedToken, err := db.GetRefreshTokenState(ctx, oldToken.ID)
	require.NoError(t, err)

	assert.False(t, updatedToken.ReplacedBy.Valid)
}

func TestSession_RefreshToken_IdleExpired(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "idle-expired@test.com",
		DisplayName: "Idle Expired Test User",
	})
	require.NoError(t, err)

	sessionRecord, err := db.CreateTestSession(
		ctx,
		sessiondbtest.CreateTestSessionParams{
			UserID:  userID,
			Platform: "web",
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  time.Now().Add(24 * time.Hour),
				Valid: true,
			},
			RevokedAt: pgtype.Timestamptz{},
		},
	)
	require.NoError(t, err)

	lastActivityAt := time.Now().Add(-2 * time.Hour)

	require.NoError(
		t,
		db.UpdateTestSessionLastActivity(
			ctx,
			sessiondbtest.UpdateTestSessionLastActivityParams{
				LastActivityAt: pgtype.Timestamptz{
					Time:  lastActivityAt,
					Valid: true,
				},
				ID: sessionRecord.ID,
			},
		),
	)

	oldHash := "idle-expired-refresh-token-hash"

	oldToken, err := db.CreateTestRefreshToken(
		ctx,
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID:  sessionRecord.ID,
			TokenHash:  oldHash,
			ReplacedBy: pgtype.UUID{},
		},
	)
	require.NoError(t, err)

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	_, _, err = repository.RefreshToken(
		ctx,
		oldHash,
		"new-refresh-token-hash",
		session.RefreshTokenPolicy{
			IdleLifetime: time.Hour,
		},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)

	assert.Equal(t, http.StatusUnauthorized, appErr.Status)
	assert.Equal(t, session.CodeSessionExpired, appErr.Code)

	// Verify the session was revoked.
	updatedSession, err := db.GetSessionState(ctx, sessionRecord.ID)
	require.NoError(t, err)

	assert.True(t, updatedSession.RevokedAt.Valid)

	// Verify the old refresh token was not replaced.
	updatedToken, err := db.GetRefreshTokenState(ctx, oldToken.ID)
	require.NoError(t, err)

	assert.False(t, updatedToken.ReplacedBy.Valid)
}