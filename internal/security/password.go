package security

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

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

// These bound what a stored hash is allowed to ask the server to do.
//
// The cost of one Argon2id derivation is roughly memory times iterations, so the
// two bounds multiply before they matter. A single verification at the ceiling
// costs about 128 MiB across eight passes — roughly five times a legitimate one.
// That is the figure that has to stay survivable, because the per-client login
// limiter bounds how many verifications a caller may *start*, not how expensive
// each one is.
//
// The ceilings are absolute rather than multiples of the constants above so that
// they cannot quietly become meaningless. TestActiveParametersFitWithinBounds
// fails if the active constants are raised past them, which turns "the verifier
// stopped accepting new hashes" into a build failure instead of a production
// incident.
//
// There are deliberately no lower bounds. A weaker-than-current hash is a real
// possibility for a credential written before the parameters were strengthened,
// and refusing to parse it would lock that user out permanently:
// PasswordVerificationResult.NeedsRehash exists but nothing acts on it yet, so
// nothing would upgrade such a hash even if it were accepted. A floor is a
// product decision about what happens to those users, not a parser decision.
const (
	// argon2MaxMemory is 128 MiB, twice the active cost. Memory decides how much
	// of the machine one verification occupies, and it is the term that guidance
	// suggests raising first, so this is the ceiling with the most room to be
	// useful.
	argon2MaxMemory uint32 = 128 * 1024

	// argon2MaxIterations is eight passes against an active three. Iterations buy
	// resistance far less per unit of CPU than memory does, so a generous multiple
	// here would let one row cost many times a legitimate verification for very
	// little extra protection.
	argon2MaxIterations uint32 = 8
)

// dummySalt and dummyPassword exist only to produce the fixed hash below.
//
// Neither is a secret, and neither is ever compared against a real credential.
// The password is not a credential anyone can hold, and the salt is a constant so
// the hash is identical on every process and every run — which is what makes it
// computable once rather than once per request.
var (
	dummySalt     = []byte("stash-it-dummy!!") // exactly argon2SaltLength
	dummyPassword = "stash-it-unknown-account"
)

const (
	phcAlgorithm = "argon2id"
	phcVersion   = "v=19"
)

// PasswordHasher hashes and verifies passwords under one shared password-work
// capacity limit.
//
// The limit is not a rate limit and is not a property of any one caller. Every
// derivation this type performs costs the same memory regardless of which
// feature asked for it or which account it was for, so the bound belongs here,
// where it cannot be forgotten by a call site: Hash, Verify and DummyVerify all
// take a slot and give it back, and a caller cannot reach the derivation without
// going through them.
type PasswordHasher struct {
	workLimiter *PasswordWorkLimiter
}

// NewPasswordHasher returns a hasher that spends the given limiter's capacity.
//
// The limiter is required rather than defaulted. A hasher with no limit would
// look exactly like one that had been given a generous one, and the difference
// only appears on an instance small enough for it to matter — which is the worst
// possible time to discover it. Passing nil therefore fails here, at wiring,
// rather than quietly disabling the protection for the life of the process.
func NewPasswordHasher(workLimiter *PasswordWorkLimiter) *PasswordHasher {
	if workLimiter == nil {
		panic(
			"security: PasswordHasher requires a PasswordWorkLimiter",
		)
	}

	return &PasswordHasher{workLimiter: workLimiter}
}

// Wait reports how long a caller waits for capacity before being refused.
//
// It is the bound the caller puts on the Retry-After it sends, which is why it
// comes from the limiter rather than being repeated at each call site: a second
// copy of this number somewhere else is a second thing that can disagree.
func (h *PasswordHasher) Wait() time.Duration {
	return h.workLimiter.wait
}

type PasswordVerificationResult struct {
	Match       bool
	NeedsRehash bool
}

func (h *PasswordHasher) Hash(
	ctx context.Context,
	password string,
) (string, error) {
	// Taken before the salt is generated, so the whole operation is inside the
	// bound rather than starting after it. Released on every path out, including
	// the error below.
	release, err := h.workLimiter.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()

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
	ctx context.Context,
	password string,
	encodedHash string,
) (PasswordVerificationResult, error) {
	params, salt, expectedHash, err := decodePHC(encodedHash)
	if err != nil {
		return PasswordVerificationResult{}, err
	}

	// Decoding happens first and costs nothing: a stored hash this build cannot
	// parse is a fault, and making it also spend capacity would let a row nobody
	// can parse hold a slot against every real request.
	release, err := h.workLimiter.Acquire(ctx)
	if err != nil {
		return PasswordVerificationResult{}, err
	}
	defer release()

	return h.verify(password, params, salt, expectedHash), nil
}

// dummyHash is a real Argon2id hash, computed once.
//
// It is built from the same constants Hash uses rather than written out as a
// literal, so it cannot fall behind the active parameters. A hand-written
// constant would keep verifying while the real credentials got stronger, and the
// whole point of it is to cost what they cost.
//
// Once is not an optimisation. Deriving a hash costs about as much as verifying
// one, so regenerating this per request would make the unknown-account path cost
// twice what the known-account path does — inverting the entire point.
var dummyHash = sync.OnceValue(func() string {
	return encodePHC(
		dummySalt,
		argon2.IDKey(
			[]byte(dummyPassword),
			dummySalt,
			argon2Iterations,
			argon2Memory,
			argon2Parallelism,
			argon2KeyLength,
		),
	)
})

// verify performs the derivation and comparison for a hash that has already been
// decoded. It assumes a slot is held, because it is what holds one.
//
// It exists so DummyVerify can do a real derivation without taking a second
// slot: routing the dummy path through Verify would make the method that holds
// capacity ask for capacity again, and at a concurrency of one that is a
// guaranteed deadlock rather than a slow request.
func (h *PasswordHasher) verify(
	password string,
	params argon2Params,
	salt []byte,
	expectedHash []byte,
) PasswordVerificationResult {
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
	}
}

// DummyVerify performs a real Argon2id verification against a fixed hash and
// discards the outcome.
//
// It exists because a login that finds no account otherwise returns before doing
// any password work, while one that finds an account and a wrong password pays
// for a full derivation. The gap between those two is large enough to tell an
// observer which addresses exist, and it also makes enumeration cheap: probing
// addresses that do not exist costs almost nothing.
//
// The result is never true for a real caller. Nothing is being authenticated
// here, and the constant password it was derived from is not one anybody holds,
// so a match would mean nothing even if it happened.
//
// It spends capacity exactly as Verify does, and it has to. The derivation is
// the same cost, so a mitigation exempt from the bound would be the cheapest way
// to make the process hold more derivations at once than it was built to.
//
// It reports failure rather than swallowing it. A caller refused here was not
// told its credentials were wrong — its password was never checked — and saying
// so would be a different answer than the one it earned.
//
// This equalizes the dominant term and nothing else. Verification still varies
// with the machine's memory behaviour, a known account adds a database row and a
// session write, and a successful login is slower still. It is a reduction in
// how much the answer can be read off the clock, not a constant-time login.
func (h *PasswordHasher) DummyVerify(
	ctx context.Context,
	password string,
) error {
	encoded := dummyHash()

	params, salt, expectedHash, err := decodePHC(encoded)
	if err != nil {
		return err
	}

	release, err := h.workLimiter.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	_ = h.verify(password, params, salt, expectedHash)

	return nil
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
			// Refused before argon2 sees it. A hash asking for more memory than
			// the ceiling would otherwise allocate it: the derivation allocates
			// memory KiB worth of 1 KiB blocks up front, so an unbounded value
			// is an unbounded allocation and the process does not survive it.
			if value > uint64(argon2MaxMemory) {
				return argon2Params{}, fmt.Errorf(
					"Argon2id memory %d exceeds the maximum of %d KiB",
					value,
					argon2MaxMemory,
				)
			}

			params.memory = uint32(value)

		case "t":
			// Refused before argon2 sees it. Iterations multiply the cost of the
			// allocation above, so this is the term that turns an already large
			// memory request into an unbounded amount of work.
			if value > uint64(argon2MaxIterations) {
				return argon2Params{}, fmt.Errorf(
					"Argon2id iterations %d exceeds the maximum of %d",
					value,
					argon2MaxIterations,
				)
			}

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
