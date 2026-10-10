package security

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/argon2"
)

// newTestHasher returns a hasher whose capacity limit is wide enough that the
// test is never the thing being measured, and finite so that every derivation
// here goes through the limiter production uses.
func newTestHasher(t *testing.T) *PasswordHasher {
	t.Helper()

	limiter, err := NewPasswordWorkLimiter(4, time.Second)
	require.NoError(t, err)

	return NewPasswordHasher(limiter)
}

func TestPasswordHasher_HashAndVerify(t *testing.T) {
	hasher := newTestHasher(t)
	password := "correct-password"

	encodedHash, err := hasher.Hash(t.Context(), password)
	require.NoError(t, err)
	require.NotEmpty(t, encodedHash)

	result, err := hasher.Verify(t.Context(), password, encodedHash)
	require.NoError(t, err)

	require.True(t, result.Match)
	require.False(t, result.NeedsRehash)
}

func TestPasswordHasher_Verify_WrongPassword(t *testing.T) {
	hasher := newTestHasher(t)

	encodedHash, err := hasher.Hash(t.Context(), "correct-password")
	require.NoError(t, err)

	result, err := hasher.Verify(t.Context(), "wrong-password", encodedHash)
	require.NoError(t, err)

	require.False(t, result.Match)
	require.False(t, result.NeedsRehash)
}

func TestPasswordHasher_Verify_InvalidHash(t *testing.T) {
	hasher := newTestHasher(t)

	result, err := hasher.Verify(t.Context(), "password", "invalid-hash")
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

	hasher := newTestHasher(t)

	result, err := hasher.Verify(t.Context(), password, encodedHash)
	require.NoError(t, err)

	require.True(t, result.Match)
	require.True(t, result.NeedsRehash)
}
