package testutil

import (
	"time"

	"github.com/thoriqr/stash-it-backend/internal/security"
)

// The password-work limits the test app runs under.
//
// They are deliberately wide. A test that is not about password work should not
// be able to fail because two of its own requests happened to hash at once, and
// a limiter tight enough to make that likely would make every timing in the suite
// depend on scheduling. A test that is about the limit asks for a different one
// and wires it up itself.
//
// What these are not is unlimited: the hasher requires a limiter, and running
// the suite with no bound at all would mean the bounded path was never what
// production took.

// TestPasswordWorkConcurrency and TestPasswordWorkWait are the limits the shared
// test app enforces.
const (
	TestPasswordWorkConcurrency = 4
	TestPasswordWorkWait        = 10 * time.Second
)

// NewPasswordWorkLimiter returns a limiter at the test defaults.
func NewPasswordWorkLimiter() *security.PasswordWorkLimiter {
	limiter, err := security.NewPasswordWorkLimiter(
		TestPasswordWorkConcurrency,
		TestPasswordWorkWait,
	)
	if err != nil {
		// The constants are compile-time checked against the constructor's
		// documented range, so this cannot fail for any build of this package.
		panic("testutil: invalid password work limiter defaults: " + err.Error())
	}

	return limiter
}

// NewPasswordHasher returns a hasher over the shared test limiter, for tests that
// need to produce or check a credential directly rather than through a route.
func NewPasswordHasher() *security.PasswordHasher {
	return security.NewPasswordHasher(NewPasswordWorkLimiter())
}
