package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Password-work settings.
//
// These bound how much expensive password work the process runs at once. They
// are configuration rather than constants because the safe number depends on the
// instance: one Argon2id derivation allocates its memory up front and holds it
// for the whole derivation, so the concurrency that fits is a fraction of the
// memory the deployment actually has. A value chosen once in a package would be
// a deployment decision wearing the costume of a constant.
const (
	// DefaultPasswordWorkConcurrency is how many derivations run at once when the
	// environment does not say.
	//
	// One is not a placeholder. It is the right answer for a constrained
	// instance, where a single 64 MiB derivation is already a large share of the
	// memory available, and it is the only value that cannot make things worse.
	// A deployment with memory to spare raises it and measures.
	DefaultPasswordWorkConcurrency = 1

	// MinPasswordWorkConcurrency is the smallest value that does any work. Zero
	// would refuse every login and every registration, which is a lockout rather
	// than a slower configuration.
	MinPasswordWorkConcurrency = 1

	// MaxPasswordWorkConcurrency is a typo guard, not a tuning value.
	//
	// It exists so a stray digit cannot ask for a number no deployment could
	// honour: at the active Argon2id parameters each derivation costs 64 MiB, so
	// even the ceiling here is far more than a small instance could hold. It is
	// deliberately generous, because a ceiling that reads like a policy would be
	// mistaken for one.
	MaxPasswordWorkConcurrency = 64

	// DefaultPasswordWorkWaitMS is how long a caller waits for capacity, in
	// milliseconds, when the environment does not say.
	//
	// It is long enough to ride out ordinary contention — two people signing in
	// at once — and short enough that a client told to come back does so while
	// the server is still in the state that made it wait.
	DefaultPasswordWorkWaitMS = 1000

	// MinPasswordWorkWaitMS is zero, which is a real choice: a deployment that
	// would rather refuse password work than hold a request open asks for it.
	MinPasswordWorkWaitMS = 0

	// MaxPasswordWorkWaitMS is a typo guard, not a tuning value. Waiting a whole
	// minute for a slot would hold a request — and the goroutine behind it —
	// longer than the request is worth keeping.
	MaxPasswordWorkWaitMS = 60_000
)

// loadPasswordWork reads and validates the password-work capacity settings.
//
// The distinction it protects is the same one WORKER_CONCURRENCY protects: a
// value that is absent is an ordinary deployment and resolves to the default,
// while a value that is present and wrong is a mistake and is reported. A typo,
// a zero where one was meant, or a number past the ceiling all mean the
// deployment said something the process is not going to do, and finding that out
// at startup is much cheaper than finding it as a lockout nobody can explain.
func loadPasswordWork() (int, time.Duration, error) {
	concurrency, err := loadBoundedInt(
		"PASSWORD_WORK_CONCURRENCY",
		DefaultPasswordWorkConcurrency,
		MinPasswordWorkConcurrency,
		MaxPasswordWorkConcurrency,
	)
	if err != nil {
		return 0, 0, err
	}

	waitMS, err := loadBoundedInt(
		"PASSWORD_WORK_WAIT_MS",
		DefaultPasswordWorkWaitMS,
		MinPasswordWorkWaitMS,
		MaxPasswordWorkWaitMS,
	)
	if err != nil {
		return 0, 0, err
	}

	return concurrency, time.Duration(waitMS) * time.Millisecond, nil
}

// loadBoundedInt reads one optional integer setting and range-checks it.
func loadBoundedInt(
	name string,
	fallback, minimum, maximum int,
) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number: %w", name, err)
	}

	if value < minimum || value > maximum {
		return 0, fmt.Errorf(
			"%s must be between %d and %d, got %d",
			name,
			minimum,
			maximum,
			value,
		)
	}

	return value, nil
}
