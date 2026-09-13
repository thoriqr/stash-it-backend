package security

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/argon2"
)

func TestPasswordHasher_HashAndVerify(t *testing.T) {
	hasher := NewPasswordHasher()
	password := "correct-password"

	encodedHash, err := hasher.Hash(password)
	require.NoError(t, err)
	require.NotEmpty(t, encodedHash)

	result, err := hasher.Verify(password, encodedHash)
	require.NoError(t, err)

	require.True(t, result.Match)
	require.False(t, result.NeedsRehash)
}

func TestPasswordHasher_Verify_WrongPassword(t *testing.T) {
	hasher := NewPasswordHasher()

	encodedHash, err := hasher.Hash("correct-password")
	require.NoError(t, err)

	result, err := hasher.Verify("wrong-password", encodedHash)
	require.NoError(t, err)

	require.False(t, result.Match)
	require.False(t, result.NeedsRehash)
}

func TestPasswordHasher_Verify_InvalidHash(t *testing.T) {
	hasher := NewPasswordHasher()

	result, err := hasher.Verify("password", "invalid-hash")
	require.Error(t, err)
	require.False(t, result.Match)
	require.False(t, result.NeedsRehash)
}

func TestPasswordHasher_Verify_NeedsRehash(t *testing.T) {
	const (
		oldMemory      uint32 = 32 * 1024
		oldIterations  uint32 = 2
		oldParallelism uint8  = 2
	)

	password := "correct-password"
	salt := []byte("0123456789abcdef")

	hash := argon2.IDKey(
		[]byte(password),
		salt,
		oldIterations,
		oldMemory,
		oldParallelism,
		32,
	)

	encodedHash := fmt.Sprintf(
		"$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		oldMemory,
		oldIterations,
		oldParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)

	hasher := NewPasswordHasher()

	result, err := hasher.Verify(password, encodedHash)
	require.NoError(t, err)

	require.True(t, result.Match)
	require.True(t, result.NeedsRehash)
}