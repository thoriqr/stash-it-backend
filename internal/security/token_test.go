package security

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateToken(t *testing.T) {
	token, err := GenerateToken()

	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.Len(t, token, 43)

	_, err = base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
}

func TestGenerateToken_Unique(t *testing.T) {
	token1, err := GenerateToken()
	require.NoError(t, err)

	token2, err := GenerateToken()
	require.NoError(t, err)

	require.NotEqual(t, token1, token2)
}

func TestHashToken(t *testing.T) {
	token := "test-token"

	hash := HashToken(token)

	require.NotEmpty(t, hash)
	require.Len(t, hash, 43)

	_, err := base64.RawURLEncoding.DecodeString(hash)
	require.NoError(t, err)
}

func TestHashToken_Deterministic(t *testing.T) {
	token := "test-token"

	hash1 := HashToken(token)
	hash2 := HashToken(token)

	require.Equal(t, hash1, hash2)
}

func TestHashToken_DifferentTokens(t *testing.T) {
	hash1 := HashToken("token-1")
	hash2 := HashToken("token-2")

	require.NotEqual(t, hash1, hash2)
}