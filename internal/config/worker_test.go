package config_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/config"
)

// These tests cover the WORKER_CONCURRENCY policy: the default, the accepted
// range including both ends, and every way a supplied value can be wrong.
//
// The distinction being protected is between a value that is absent and a value
// that is wrong. An absent one is an ordinary deployment and resolves to the
// default. A wrong one is a mistake, and the loader reports it rather than
// quietly substituting a number the operator did not choose.
//
// Every case goes through LoadWorker rather than the unexported helper, so what
// is asserted is what the worker binary actually receives.

// loadConcurrencyWith sets WORKER_CONCURRENCY and loads the worker config.
//
// The other required values are set too, so a case asserting on concurrency is
// not also asserting that the rest of the configuration happens to be present.
// Whether the value is empty or not is passed through, which is how the absent
// case is expressed.
func loadConcurrencyWith(t *testing.T, value string) (config.WorkerConfig, error) {
	t.Helper()

	setWorkerEnv(t)
	t.Setenv("WORKER_CONCURRENCY", value)

	return config.LoadWorker()
}

// setWorkerEnv makes the required worker values present for the duration of a
// test. godotenv does not override variables that are already set, so these win
// over any .env file the loader finds.
func setWorkerEnv(t *testing.T) {
	t.Helper()

	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
}

// The bounds are the policy, so they are asserted rather than assumed. A silent
// change to any of them would change what a deployment gets.
func TestWorkerConcurrencyBounds(t *testing.T) {
	require.Equal(t, 5, config.DefaultWorkerConcurrency)
	require.Equal(t, 1, config.MinWorkerConcurrency)
	require.Equal(t, 20, config.MaxWorkerConcurrency)

	require.LessOrEqual(
		t,
		config.MinWorkerConcurrency,
		config.DefaultWorkerConcurrency,
		"the default has to be a value the loader accepts",
	)
	require.LessOrEqual(
		t,
		config.DefaultWorkerConcurrency,
		config.MaxWorkerConcurrency,
		"the default has to be within the accepted range",
	)
}

// An absent value is not an error. The default applies, and the rest of the
// configuration still loads.
func TestLoadWorker_UnsetConcurrencyUsesTheDefault(t *testing.T) {
	// Setenv is what makes the variable genuinely absent for this test, so a
	// value in the developer's own environment cannot leak into it.
	t.Setenv("WORKER_CONCURRENCY", "")
	require.NoError(t, os.Unsetenv("WORKER_CONCURRENCY"))

	cfg, err := loadConcurrencyWith(t, "")

	require.NoError(t, err)
	require.Equal(t, config.DefaultWorkerConcurrency, cfg.WorkerConcurrency)
}

// Any value inside the range is taken as given. Concurrency is a deployment
// knob, so a value between the ends is honoured rather than adjusted.
func TestLoadWorker_AcceptsValuesInRange(t *testing.T) {
	cases := []int{
		2,
		3,
		7,
		10,
		19,
	}

	for _, value := range cases {
		t.Run(strconv.Itoa(value), func(t *testing.T) {
			cfg, err := loadConcurrencyWith(t, strconv.Itoa(value))

			require.NoError(t, err)
			require.Equal(t, value, cfg.WorkerConcurrency)
		})
	}
}

// The ends of the range are inside it. Each is asserted separately because the
// bounds are where an off-by-one would hide.
func TestLoadWorker_AcceptsTheRangeBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		value int
	}{
		{"minimum", config.MinWorkerConcurrency},
		{"maximum", config.MaxWorkerConcurrency},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadConcurrencyWith(t, strconv.Itoa(tc.value))

			require.NoError(t, err)
			require.Equal(t, tc.value, cfg.WorkerConcurrency)
		})
	}
}

// Zero and negative values are refused rather than replaced. Asynq reads a
// non-positive concurrency as "use the CPU count", which is the opposite of what
// somebody asking for zero meant, so accepting the value would be worse than
// refusing it.
func TestLoadWorker_RejectsNonPositiveConcurrency(t *testing.T) {
	for _, value := range []string{"0", "-1", "-20"} {
		t.Run(value, func(t *testing.T) {
			_, err := loadConcurrencyWith(t, value)

			require.Error(
				t,
				err,
				"a concurrency outside the range must fail startup",
			)
			require.ErrorContains(t, err, "WORKER_CONCURRENCY")
			require.ErrorContains(t, err, "between")
		})
	}
}

// Past the ceiling is refused for the same reason. A worker running far more
// concurrent attempts than intended holds more outbound requests and database
// connections open while the attempts behind them wait.
func TestLoadWorker_RejectsConcurrencyAboveTheMaximum(t *testing.T) {
	for _, value := range []string{"21", "100", "100000"} {
		t.Run(value, func(t *testing.T) {
			_, err := loadConcurrencyWith(t, value)

			require.Error(t, err)
			require.ErrorContains(t, err, "WORKER_CONCURRENCY")
			require.ErrorContains(t, err, "between")
		})
	}
}

// A value that is not a number is refused rather than ignored. Reading it as
// unset would start a worker at the default while the deployment believes it is
// running at whatever it asked for.
func TestLoadWorker_RejectsNonNumericConcurrency(t *testing.T) {
	for _, value := range []string{
		"many",
		"5.5",
		"1e3",
		" 5",
		"5 ",
		"0x5",
		"+",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := loadConcurrencyWith(t, value)

			require.Error(
				t,
				err,
				"an unparseable concurrency must fail startup",
			)
			require.ErrorContains(t, err, "WORKER_CONCURRENCY")
			require.ErrorContains(t, err, "whole number")
		})
	}
}

// A refused concurrency is reported before anything else is trusted, and no
// configuration is returned alongside the error. A caller that ignored the error
// would otherwise get a zero value and pass it to Asynq.
func TestLoadWorker_ConcurrencyErrorReturnsNoConfig(t *testing.T) {
	cfg, err := loadConcurrencyWith(t, "500")

	require.Error(t, err)
	require.Equal(
		t,
		config.WorkerConfig{},
		cfg,
		"a rejected configuration must come back empty",
	)
}

// Concurrency is validated by the loader, so a bad value is a startup failure
// rather than something the worker discovers later. This is asserted against
// LoadWorker because that is the boundary the binary uses, and the other required
// values are present so the error cannot be about one of those instead.
func TestLoadWorker_ConcurrencyIsValidatedAsPartOfLoading(t *testing.T) {
	_, err := loadConcurrencyWith(t, "not-a-number")

	require.Error(t, err)
	require.ErrorContains(
		t,
		err,
		"WORKER_CONCURRENCY",
		"the concurrency policy is checked as part of loading the configuration",
	)
}

// The other required values are still validated, so this change did not trade one
// startup failure for another.
func TestLoadWorker_StillRequiresDatabaseAndRedis(t *testing.T) {
	t.Run("a missing database url is still an error", func(t *testing.T) {
		setWorkerEnv(t)
		t.Setenv("WORKER_CONCURRENCY", "5")

		require.NoError(t, os.Unsetenv("DATABASE_URL"))

		_, err := config.LoadWorker()

		require.Error(t, err)
		require.ErrorContains(t, err, "DATABASE_URL")
	})

	t.Run("a missing redis url is still an error", func(t *testing.T) {
		setWorkerEnv(t)
		t.Setenv("WORKER_CONCURRENCY", "5")

		require.NoError(t, os.Unsetenv("REDIS_URL"))

		_, err := config.LoadWorker()

		require.Error(t, err)
		require.ErrorContains(t, err, "REDIS_URL")
	})
}
