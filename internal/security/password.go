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

	return encodePHC(salt, hash), nil
}

func (h *PasswordHasher) Verify(password, encodedHash string) (bool, error) {
	params, salt, expectedHash, err := decodePHC(encodedHash)
	if err != nil {
		return false, err
	}

	actualHash := argon2.IDKey(
		[]byte(password),
		salt,
		params.iterations,
		params.memory,
		params.parallelism,
		uint32(len(expectedHash)),
	)

	return subtle.ConstantTimeCompare(actualHash, expectedHash) == 1, nil
}

type argon2Params struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
}

func encodePHC(salt, hash []byte) string {
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

	if len(parts) != 6 {
		return argon2Params{}, nil, nil, errors.New(
			"invalid password hash format",
		)
	}

	if parts[1] != phcAlgorithm {
		return argon2Params{}, nil, nil, errors.New(
			"unsupported password hash algorithm",
		)
	}

	if parts[2] != phcVersion {
		return argon2Params{}, nil, nil, errors.New(
			"unsupported Argon2 version",
		)
	}

	params, err := parseArgon2Params(parts[3])
	if err != nil {
		return argon2Params{}, nil, nil, err
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return argon2Params{}, nil, nil, errors.New(
			"invalid password hash salt",
		)
	}

	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return argon2Params{}, nil, nil, errors.New(
			"invalid password hash",
		)
	}

	if len(salt) == 0 || len(hash) == 0 {
		return argon2Params{}, nil, nil, errors.New(
			"invalid password hash",
		)
	}

	return params, salt, hash, nil
}

func parseArgon2Params(value string) (argon2Params, error) {
	var params argon2Params

	for _, part := range strings.Split(value, ",") {
		key, rawValue, ok := strings.Cut(part, "=")

		if !ok {
			return argon2Params{}, errors.New(
				"invalid Argon2 parameters",
			)
		}

		switch key {
		case "m":
			memory, err := strconv.ParseUint(rawValue, 10, 32)
			if err != nil {
				return argon2Params{}, errors.New(
					"invalid Argon2 memory",
				)
			}

			params.memory = uint32(memory)

		case "t":
			iterations, err := strconv.ParseUint(rawValue, 10, 32)
			if err != nil {
				return argon2Params{}, errors.New(
					"invalid Argon2 iterations",
				)
			}

			params.iterations = uint32(iterations)

		case "p":
			parallelism, err := strconv.ParseUint(rawValue, 10, 8)
			if err != nil {
				return argon2Params{}, errors.New(
					"invalid Argon2 parallelism",
				)
			}

			params.parallelism = uint8(parallelism)

		default:
			return argon2Params{}, errors.New(
				"unsupported Argon2 parameter",
			)
		}
	}

	if params.memory == 0 ||
		params.iterations == 0 ||
		params.parallelism == 0 {
		return argon2Params{}, errors.New(
			"invalid Argon2 parameters",
		)
	}

	return params, nil
}