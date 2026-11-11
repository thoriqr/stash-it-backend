package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadEnvFile_MissingFileIsNotAnError(t *testing.T) {
	t.Chdir(t.TempDir())

	require.NoError(t, loadEnvFile("production"))
}

func TestLoadEnvFile_LoadsAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	const key = "STASH_IT_ENV_FILE_TEST"

	require.NoError(t, os.Unsetenv(key))
	t.Cleanup(func() {
		_ = os.Unsetenv(key)
	})

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".env.production"),
		[]byte(key+"=loaded\n"),
		0o600,
	))

	require.NoError(t, loadEnvFile("production"))
	require.Equal(t, "loaded", os.Getenv(key))
}

func TestLoadEnvFile_ReportsABrokenFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// A directory cannot be read as an env file and is not os.ErrNotExist, so it
	// must be reported rather than silently treated as absent.
	require.NoError(t, os.Mkdir(
		filepath.Join(dir, ".env.production"),
		0o700,
	))

	require.Error(t, loadEnvFile("production"))
}

func setMinimalLoadEnv(t *testing.T) {
	t.Helper()

	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/stash_it?sslmode=disable")
	t.Setenv("VERIFICATION_CODE_SECRET", "verification-secret")
	t.Setenv("ACCESS_TOKEN_SECRET", "access-secret")
	t.Setenv("GOOGLE_CLIENT_ID", "client-id")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("EMAIL_PROVIDER", "none")
	t.Setenv("CLIENT_IP_SOURCE", "peer")

	// Clear optional values that may be present in the developer environment so
	// the test asserts the loader, not the machine.
	for _, key := range []string{
		"GOOGLE_DESKTOP_CLIENT_ID",
		"SESSION_CLEANUP_SECRET",
		"TRUSTED_PROXIES",
		"TRUSTED_PROXY_HEADER",
		"PASSWORD_WORK_CONCURRENCY",
		"PASSWORD_WORK_WAIT_MS",
		"PIN_EMAIL_BUDGET_PER_EMAIL_MAX",
		"PIN_EMAIL_BUDGET_GLOBAL_MAX",
		"PIN_EMAIL_BUDGET_WINDOW_SECONDS",
		"EMAIL_TIMEOUT_SECONDS",
		"BREVO_API_KEY",
		"EMAIL_FROM_ADDRESS",
		"EMAIL_FROM_NAME",
		"SENTRY_DSN",
		"SENTRY_ENVIRONMENT",
		"SENTRY_RELEASE",
		"SENTRY_SAMPLE_RATE",
		"SENTRY_TRACES_SAMPLE_RATE",
		"SENTRY_DEBUG",
		"WORKER_CONCURRENCY",
	} {
		t.Setenv(key, "")
	}
}

// The API loader accepts a deployment that supplies variables directly through
// the process environment, with no .env.<APP_ENV> file in the working directory.
func TestLoad_ToleratesMissingEnvFileWhenProcessEnvIsSet(t *testing.T) {
	t.Chdir(t.TempDir())
	setMinimalLoadEnv(t)

	cfg, err := Load()

	require.NoError(t, err)
	require.Equal(t, "production", cfg.AppEnv)
	require.Equal(
		t,
		"postgres://user:pass@localhost:5432/stash_it?sslmode=disable",
		cfg.DatabaseURL,
	)
}

// Tolerating a missing file must not weaken validation. A required variable that
// is absent still fails with the specific error.
func TestLoad_StillReportsAMissingRequiredValueWithoutEnvFile(t *testing.T) {
	t.Chdir(t.TempDir())
	setMinimalLoadEnv(t)
	t.Setenv("DATABASE_URL", "")

	_, err := Load()

	require.Error(t, err)
	require.Contains(t, err.Error(), "DATABASE_URL is required")
}

// The worker loader uses the same env-file helper, so it keeps working without a
// file as well.
func TestLoadWorker_ToleratesMissingEnvFile(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/stash_it?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("WORKER_CONCURRENCY", "3")

	cfg, err := LoadWorker()

	require.NoError(t, err)
	require.Equal(t, 3, cfg.WorkerConcurrency)
}
