package security

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/argon2"
)

// Bounds on what a stored hash may ask the server to do.
//
// The proof that a bound is enforced before the derivation is reached is that the
// test survives at all: argon2.IDKey allocates its memory up front, so a hash
// asking for four tebibytes and reaching it takes the process down rather than
// merely failing an assertion. A test that dies on regression is a stronger proof
// than one that measures how long a rejection took, and it cannot be defeated by
// a slow or a loaded machine.

// phc renders a PHC string from an already-derived hash and the parameters the
// string claims.
func phc(salt, hash []byte, memory, iterations, parallelism uint32) string {
	return fmt.Sprintf(
		"$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		memory,
		iterations,
		parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)
}

// encodeAt derives for real at the given parameters and claims those same
// parameters, so a round trip through Verify must match.
func encodeAt(
	password string,
	salt []byte,
	memory uint32,
	iterations uint32,
	parallelism uint32,
) string {
	return phc(
		salt,
		argon2.IDKey(
			[]byte(password),
			salt,
			iterations,
			memory,
			uint8(parallelism),
			argon2KeyLength,
		),
		memory,
		iterations,
		parallelism,
	)
}

// encodeClaiming writes a hash whose parameters say something other than what it
// was derived at.
//
// It is how a rejection is staged without staging the allocation the parameter
// asks for: the string is well formed and carries a real, correctly padded hash,
// so the only thing wrong with it is the cost it requests. If the bound were not
// enforced first, argon2 would be asked to honour the claim rather than the
// parameters the bytes were actually derived at.
func encodeClaiming(
	password string,
	salt []byte,
	memory uint32,
	iterations uint32,
	parallelism uint32,
) string {
	return phc(
		salt,
		argon2.IDKey(
			[]byte(password),
			salt,
			argon2Iterations,
			argon2Memory,
			argon2Parallelism,
			argon2KeyLength,
		),
		memory,
		iterations,
		parallelism,
	)
}

var boundsTestSalt = []byte("0123456789abcdef")

func TestPasswordHasher_RejectsMemoryAboveTheCeiling(t *testing.T) {
	hasher := NewPasswordHasher()

	for _, memory := range []uint32{
		argon2MaxMemory + 1,
		2 * argon2MaxMemory,
		1024 * 1024,
		^uint32(0),
	} {
		t.Run(fmt.Sprintf("m=%d", memory), func(t *testing.T) {
			result, err := hasher.Verify("password", encodeClaiming(
				"password",
				boundsTestSalt,
				memory,
				argon2Iterations,
				uint32(argon2Parallelism),
			))

			require.Error(t, err)
			require.False(t, result.Match)
			require.Contains(t, err.Error(), "memory")
		})
	}
}

func TestPasswordHasher_RejectsIterationsAboveTheCeiling(t *testing.T) {
	hasher := NewPasswordHasher()

	for _, iterations := range []uint32{
		argon2MaxIterations + 1,
		100,
		^uint32(0),
	} {
		t.Run(fmt.Sprintf("t=%d", iterations), func(t *testing.T) {
			result, err := hasher.Verify("password", encodeClaiming(
				"password",
				boundsTestSalt,
				argon2Memory,
				iterations,
				uint32(argon2Parallelism),
			))

			require.Error(t, err)
			require.False(t, result.Match)
			require.Contains(t, err.Error(), "iterations")
		})
	}
}

// The parallelism guard predates the memory and iteration ceilings, but it is the
// same kind of guard: a value above 255 cannot be held in the uint8 the
// parameters are stored in, so accepting it would derive with a different
// parallelism than the one the hash was written with.
func TestPasswordHasher_RejectsParallelismAboveTheCeiling(t *testing.T) {
	hasher := NewPasswordHasher()

	result, err := hasher.Verify("password", encodeClaiming(
		"password",
		boundsTestSalt,
		argon2Memory,
		argon2Iterations,
		256,
	))

	require.Error(t, err)
	require.False(t, result.Match)
}

// The ceilings are inclusive. A hash sitting exactly on a bound is a legitimate
// hash — a machine twice as strong as this one could have written it — and
// refusing it would turn a security measure into an availability one.
func TestPasswordHasher_AcceptsParametersOnTheCeilings(t *testing.T) {
	hasher := NewPasswordHasher()

	const password = "password"

	cases := map[string]struct {
		memory      uint32
		iterations  uint32
		parallelism uint32
	}{
		"the active parameters": {
			memory:      argon2Memory,
			iterations:  argon2Iterations,
			parallelism: uint32(argon2Parallelism),
		},
		"memory on the ceiling": {
			memory:      argon2MaxMemory,
			iterations:  argon2Iterations,
			parallelism: uint32(argon2Parallelism),
		},
		"iterations on the ceiling": {
			memory:      argon2Memory,
			iterations:  argon2MaxIterations,
			parallelism: uint32(argon2Parallelism),
		},
	}

	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := hasher.Verify(password, encodeAt(
				password,
				boundsTestSalt,
				params.memory,
				params.iterations,
				params.parallelism,
			))

			require.NoError(t, err)
			require.True(t, result.Match)
		})
	}
}

// There are deliberately no lower bounds, so a hash weaker than the active
// parameters must still verify. Anything else would lock out a credential written
// before the parameters were strengthened, and NeedsRehash exists but nothing
// acts on it, so there would be no way back in short of a reset.
func TestPasswordHasher_AcceptsParametersBelowTheActiveCost(t *testing.T) {
	hasher := NewPasswordHasher()

	const (
		password              = "password"
		weakMemory     uint32 = 8 * 1024
		weakIterations uint32 = 1
	)

	result, err := hasher.Verify(password, encodeAt(
		password,
		boundsTestSalt,
		weakMemory,
		weakIterations,
		1,
	))

	require.NoError(t, err)
	require.True(t, result.Match)
	require.True(t, result.NeedsRehash)
}

// TestActiveParametersFitWithinBounds is the one named in the note on
// argon2MaxMemory. If the active parameters are ever raised past a ceiling, Hash
// would keep producing credentials this build cannot verify — a failure that
// would otherwise only show up as every user locked out, in production.
func TestActiveParametersFitWithinBounds(t *testing.T) {
	require.LessOrEqual(
		t,
		argon2Memory,
		argon2MaxMemory,
		"the active memory is above the ceiling: this build would produce "+
			"credentials it cannot verify",
	)

	require.LessOrEqual(
		t,
		argon2Iterations,
		argon2MaxIterations,
		"the active iteration count is above the ceiling: this build would "+
			"produce credentials it cannot verify",
	)
}
