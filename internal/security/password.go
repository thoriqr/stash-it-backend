package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	// These are the current Argon2id parameters used for new password hashes.
	// If these parameters are strengthened later, existing hashes can still
	// be verified because their original parameters are stored in the hash.
	argon2Memory      uint32 = 64 * 1024
	argon2Iterations  uint32 = 3
	argon2Parallelism uint8  = 4
	argon2SaltLength         = 16
	argon2KeyLength          = 32
)

const (
	phcAlgorithm = "argon2id"
	phcVersion   = "v=19"
)

type PasswordHasher struct{}

func NewPasswordHasher() *PasswordHasher {
	return &PasswordHasher{}
}

type PasswordVerificationResult struct {
	Match       bool
	NeedsRehash bool
}

func (h *PasswordHasher) Hash(password string) (string, error) {
	salt := make([]byte, argon2SaltLength)

	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}

	hash := argon2.IDKey(
		[]byte(password),
		salt,
		argon2Iterations,
		argon2Memory,
		argon2Parallelism,
		argon2KeyLength,
	)

	// Store the Argon2id parameters, salt, and hash together so that
	// Verify can reconstruct the exact parameters used for this hash.
	return encodePHC(salt, hash), nil
}

func (h *PasswordHasher) Verify(
	password string,
	encodedHash string,
) (PasswordVerificationResult, error) {
	params, salt, expectedHash, err := decodePHC(encodedHash)
	if err != nil {
		return PasswordVerificationResult{}, err
	}

	// Use the parameters stored with the hash instead of the current
	// defaults so that older password hashes remain verifiable after
	// the application's Argon2id parameters are strengthened.
	actualHash := argon2.IDKey(
		[]byte(password),
		salt,
		params.iterations,
		params.memory,
		params.parallelism,
		uint32(len(expectedHash)),
	)

	match := subtle.ConstantTimeCompare(actualHash, expectedHash) == 1

	// Rehashing is optional and is not performed here.
	// A future login flow can use NeedsRehash after a successful
	// verification to silently upgrade an outdated password hash
	// without requiring the user to change their password.
	return PasswordVerificationResult{
		Match:       match,
		NeedsRehash: match && needsRehash(params),
	}, nil
}

type argon2Params struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
}

func needsRehash(params argon2Params) bool {
	return params.memory < argon2Memory ||
		params.iterations < argon2Iterations ||
		params.parallelism < argon2Parallelism
}

func encodePHC(salt, hash []byte) string {
	// PHC format keeps the algorithm, version, parameters, salt, and hash
	// in a single self-contained string suitable for database storage.
	return fmt.Sprintf(
		"$%s$%s$m=%d,t=%d,p=%d$%s$%s",
		phcAlgorithm,
		phcVersion,
		argon2Memory,
		argon2Iterations,
		argon2Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)
}

func decodePHC(encoded string) (argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")

	if len(parts) != 6 || parts[0] != "" {
		return argon2Params{}, nil, nil, errors.New("invalid password hash format")
	}

	if parts[1] != phcAlgorithm {
		return argon2Params{}, nil, nil, errors.New("unsupported password hash algorithm")
	}

	if parts[2] != phcVersion {
		return argon2Params{}, nil, nil, errors.New("unsupported password hash version")
	}

	params, err := parseArgon2Params(parts[3])
	if err != nil {
		return argon2Params{}, nil, nil, err
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return argon2Params{}, nil, nil, errors.New("invalid password hash salt")
	}

	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(hash) == 0 {
		return argon2Params{}, nil, nil, errors.New("invalid password hash")
	}

	return params, salt, hash, nil
}

func parseArgon2Params(encoded string) (argon2Params, error) {
	var params argon2Params

	for _, part := range strings.Split(encoded, ",") {
		keyValue := strings.SplitN(part, "=", 2)
		if len(keyValue) != 2 {
			return argon2Params{}, errors.New("invalid Argon2id parameters")
		}

		value, err := strconv.ParseUint(keyValue[1], 10, 32)
		if err != nil || value == 0 {
			return argon2Params{}, errors.New("invalid Argon2id parameter value")
		}

		switch keyValue[0] {
		case "m":
			params.memory = uint32(value)

		case "t":
			params.iterations = uint32(value)

		case "p":
			if value > 255 {
				return argon2Params{}, errors.New("invalid Argon2id parallelism")
			}
			params.parallelism = uint8(value)

		default:
			return argon2Params{}, errors.New("unsupported Argon2id parameter")
		}
	}

	if params.memory == 0 ||
		params.iterations == 0 ||
		params.parallelism == 0 {
		return argon2Params{}, errors.New("incomplete Argon2id parameters")
	}

	return params, nil
}