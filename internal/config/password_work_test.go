package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The password-work capacity settings: the default, the accepted range including
// both ends, and every way a supplied value can be wrong.
//
// The distinction protected is the one WORKER_CONCURRENCY protects. An absent
// value is an ordinary deployment and resolves to the default. A value that is
// present and wrong is a mistake, and is reported rather than quietly replaced
// with a number nobody chose.
//
// These go through loadPasswordWork rather than the exported loader because the
// rest of Load() needs a whole set of unrelated required values, and a case about
// password work is not a case about those.

// loadPasswordWorkWith sets the two variables and loads them.
//
// Each is set independently rather than together, because they are two numbers
// and a case asserting on one is not asserting on the other.
func loadPasswordWorkWith(
	t *testing.T,
	concurrency string,
	waitMS string,
) (int, time.Duration, error) {
	t.Helper()

	t.Setenv("PASSWORD_WORK_CONCURRENCY", concurrency)
	t.Setenv("PASSWORD_WORK_WAIT_MS", waitMS)

	return loadPasswordWork()
}

// The baseline is one concurrent derivation and a one second wait, which is the
// configuration a constrained instance should run without anything having to be
// measured first.
func TestLoadPasswordWork_DefaultsToTheConstrainedBaseline(t *testing.T) {
	concurrency, wait, err := loadPasswordWork()
	require.NoError(t, err)

	require.Equal(t, DefaultPasswordWorkConcurrency, concurrency)
	require.Equal(t, 1, concurrency)
	require.Equal(t, time.Second, wait)
}

// A value that is set and valid is used, at both ends of each range. Zero wait
// is a real choice — refuse rather than hold a request open — so it is inside
// the range rather than outside it.
func TestLoadPasswordWork_AcceptsExplicitValuesAcrossTheRange(t *testing.T) {
	cases := map[string]struct {
		concurrency     string
		waitMS          string
		wantConcurrency int
		wantWait        time.Duration
	}{
		"the smallest concurrency that does work": {
			concurrency: "1", waitMS: "0",
			wantConcurrency: 1, wantWait: 0,
		},
		"a larger deployment": {
			concurrency: "8", waitMS: "250",
			wantConcurrency: 8, wantWait: 250 * time.Millisecond,
		},
		"the concurrency ceiling": {
			concurrency: "64", waitMS: "1000",
			wantConcurrency: 64, wantWait: time.Second,
		},
		"the wait ceiling": {
			concurrency: "2", waitMS: "60000",
			wantConcurrency: 2, wantWait: time.Minute,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			concurrency, wait, err := loadPasswordWorkWith(
				t,
				tc.concurrency,
				tc.waitMS,
			)

			require.NoError(t, err)
			require.Equal(t, tc.wantConcurrency, concurrency)
			require.Equal(t, tc.wantWait, wait)
		})
	}
}

// Every way a supplied value can be wrong is reported. None is corrected,
// because a deployment that believes it asked for something it did not get is
// worse than one that refuses to start.
func TestLoadPasswordWork_RejectsAnUnusableValue(t *testing.T) {
	cases := map[string]struct {
		concurrency string
		waitMS      string
		errContains string
	}{
		"zero concurrency would lock every login out": {
			concurrency: "0", waitMS: "1000",
			errContains: "PASSWORD_WORK_CONCURRENCY must be between 1 and 64",
		},
		"a negative concurrency": {
			concurrency: "-4", waitMS: "1000",
			errContains: "PASSWORD_WORK_CONCURRENCY must be between 1 and 64",
		},
		"a concurrency past the typo guard": {
			concurrency: "60000", waitMS: "1000",
			errContains: "PASSWORD_WORK_CONCURRENCY must be between 1 and 64",
		},
		"a concurrency that is not a number": {
			concurrency: "two", waitMS: "1000",
			errContains: "PASSWORD_WORK_CONCURRENCY must be a whole number",
		},
		"a negative wait": {
			concurrency: "1", waitMS: "-1",
			errContains: "PASSWORD_WORK_WAIT_MS must be between 0 and 60000",
		},
		"a wait past the typo guard": {
			concurrency: "1", waitMS: "3600000",
			errContains: "PASSWORD_WORK_WAIT_MS must be between 0 and 60000",
		},
		"a wait that is not a number": {
			concurrency: "1", waitMS: "a second",
			errContains: "PASSWORD_WORK_WAIT_MS must be a whole number",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := loadPasswordWorkWith(t, tc.concurrency, tc.waitMS)

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errContains)
		})
	}
}

// The concurrency is validated before the wait, so a deployment with both wrong
// is told about the one that would lock people out rather than the one that would
// merely hold a request open.
func TestLoadPasswordWork_ReportsConcurrencyBeforeWait(t *testing.T) {
	_, _, err := loadPasswordWorkWith(t, "0", "also-wrong")

	require.Error(t, err)
	require.Contains(t, err.Error(), "PASSWORD_WORK_CONCURRENCY")
}
