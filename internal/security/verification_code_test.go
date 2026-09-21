package security

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerificationCodeHasher_Generate(t *testing.T) {
	hasher := NewVerificationCodeHasher([]byte("test-secret"))

	code, err := hasher.Generate()

	require.NoError(t, err)
	require.Len(t, code, verificationCodeLength)
	require.Regexp(t, regexp.MustCompile(`^\d{6}$`), code)
}

func TestVerificationCodeHasher_Hash(t *testing.T) {
	hasher := NewVerificationCodeHasher([]byte("test-secret"))

	hash1 := hasher.Hash("123456")
	hash2 := hasher.Hash("123456")

	require.NotEmpty(t, hash1)
	require.Equal(t, hash1, hash2)
}

func TestVerificationCodeHasher_Hash_DifferentCodes(t *testing.T) {
	hasher := NewVerificationCodeHasher([]byte("test-secret"))

	hash1 := hasher.Hash("123456")
	hash2 := hasher.Hash("654321")

	require.NotEqual(t, hash1, hash2)
}

func TestVerificationCodeHasher_Verify(t *testing.T) {
	hasher := NewVerificationCodeHasher([]byte("test-secret"))

	code := "123456"
	hash := hasher.Hash(code)

	require.True(t, hasher.Verify(code, hash))
	require.False(t, hasher.Verify("654321", hash))
}

func TestVerificationCodeHasher_Verify_InvalidHash(t *testing.T) {
	hasher := NewVerificationCodeHasher([]byte("test-secret"))

	require.False(t, hasher.Verify("123456", "invalid-hash"))
}