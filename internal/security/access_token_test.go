package security

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAccessToken_GenerateAndVerify(t *testing.T) {
	secret := []byte("test-secret")
	generator := NewAccessTokenGenerator(secret)
	verifier := NewAccessTokenVerifier(secret)

	userID := uuid.New()
	sessionID := uuid.New()
	lifetime := time.Hour

	token, err := generator.Generate(userID, sessionID, lifetime)

	require.NoError(t, err)
	require.NotEmpty(t, token)

	claims, err := verifier.Verify(token)

	require.NoError(t, err)
	require.Equal(t, userID.String(), claims.Subject)
	require.Equal(t, sessionID, claims.SessionID)
	require.NotNil(t, claims.IssuedAt)
	require.NotNil(t, claims.ExpiresAt)
	require.WithinDuration(t, time.Now().Add(lifetime), claims.ExpiresAt.Time, 2*time.Second)
}

func TestAccessToken_Verify_Expired(t *testing.T) {
	secret := []byte("test-secret")
	generator := NewAccessTokenGenerator(secret)
	verifier := NewAccessTokenVerifier(secret)

	token, err := generator.Generate(
		uuid.New(),
		uuid.New(),
		-time.Second,
	)

	require.NoError(t, err)

	_, err = verifier.Verify(token)

	require.Error(t, err)
	require.ErrorIs(t, err, jwt.ErrTokenExpired)
}

func TestAccessToken_Verify_InvalidSignature(t *testing.T) {
	generator := NewAccessTokenGenerator([]byte("correct-secret"))
	verifier := NewAccessTokenVerifier([]byte("wrong-secret"))

	token, err := generator.Generate(
		uuid.New(),
		uuid.New(),
		time.Hour,
	)

	require.NoError(t, err)

	_, err = verifier.Verify(token)

	require.Error(t, err)
	require.ErrorIs(t, err, jwt.ErrTokenSignatureInvalid)
}

func TestAccessToken_Verify_InvalidSigningMethod(t *testing.T) {
	secret := []byte("test-secret")
	verifier := NewAccessTokenVerifier(secret)

	token := jwt.NewWithClaims(
		jwt.SigningMethodHS384,
		AccessTokenClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   uuid.New().String(),
				IssuedAt:  jwt.NewNumericDate(time.Now()),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
			SessionID: uuid.New(),
		},
	)

	tokenString, err := token.SignedString(secret)

	require.NoError(t, err)

	_, err = verifier.Verify(tokenString)

	require.Error(t, err)
	require.ErrorIs(t, err, jwt.ErrTokenSignatureInvalid)
}

func TestAccessToken_Verify_MissingSubject(t *testing.T) {
	secret := []byte("test-secret")
	verifier := NewAccessTokenVerifier(secret)

	token := jwt.NewWithClaims(
		jwt.SigningMethodHS256,
		AccessTokenClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				IssuedAt:  jwt.NewNumericDate(time.Now()),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
			SessionID: uuid.New(),
		},
	)

	tokenString, err := token.SignedString(secret)

	require.NoError(t, err)

	_, err = verifier.Verify(tokenString)

	require.Error(t, err)
	require.ErrorIs(t, err, jwt.ErrTokenRequiredClaimMissing)
}

func TestAccessToken_Verify_MissingSessionID(t *testing.T) {
	secret := []byte("test-secret")
	verifier := NewAccessTokenVerifier(secret)

	token := jwt.NewWithClaims(
		jwt.SigningMethodHS256,
		AccessTokenClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   uuid.New().String(),
				IssuedAt:  jwt.NewNumericDate(time.Now()),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
		},
	)

	tokenString, err := token.SignedString(secret)

	require.NoError(t, err)

	_, err = verifier.Verify(tokenString)

	require.Error(t, err)
	require.ErrorIs(t, err, jwt.ErrTokenRequiredClaimMissing)
}