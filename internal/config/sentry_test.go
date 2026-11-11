package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func loadSentryWith(
	t *testing.T,
	appEnv string,
	dsn string,
	environment string,
	release string,
	sampleRate string,
	tracesSampleRate string,
	debug string,
) (SentryConfig, error) {
	t.Helper()

	t.Setenv("SENTRY_DSN", dsn)
	t.Setenv("SENTRY_ENVIRONMENT", environment)
	t.Setenv("SENTRY_RELEASE", release)
	t.Setenv("SENTRY_SAMPLE_RATE", sampleRate)
	t.Setenv("SENTRY_TRACES_SAMPLE_RATE", tracesSampleRate)
	t.Setenv("SENTRY_DEBUG", debug)

	return loadSentry(appEnv)
}

// An empty DSN is the ordinary state: Sentry is disabled and the application
// still runs. This is what local development and a deployment that has not
// created a Sentry project get.
func TestLoadSentry_EmptyDSNDisablesReporting(t *testing.T) {
	cfg, err := loadSentryWith(
		t, "development", "", "", "", "", "", "",
	)

	require.NoError(t, err)
	require.Empty(t, cfg.DSN)
	require.Equal(t, "development", cfg.Environment)
	require.Equal(t, 1.0, cfg.SampleRate)
	require.Equal(t, 0.0, cfg.TracesSampleRate)
	require.False(t, cfg.Debug)
}

// A complete configuration resolves to the values the SDK is initialized with.
func TestLoadSentry_AcceptsACompleteConfiguration(t *testing.T) {
	cfg, err := loadSentryWith(
		t,
		"production",
		"  https://key@example.ingest.sentry.io/1  ",
		"staging",
		"  abc123  ",
		"0.25",
		"0.1",
		"true",
	)

	require.NoError(t, err)
	require.Equal(t, "https://key@example.ingest.sentry.io/1", cfg.DSN)
	require.Equal(t, "staging", cfg.Environment)
	require.Equal(t, "abc123", cfg.Release)
	require.Equal(t, 0.25, cfg.SampleRate)
	require.Equal(t, 0.1, cfg.TracesSampleRate)
	require.True(t, cfg.Debug)
}

// An unset environment inherits APP_ENV rather than inventing a name.
func TestLoadSentry_EnvironmentDefaultsToAppEnv(t *testing.T) {
	cfg, err := loadSentryWith(
		t, "production", "", "", "", "", "", "",
	)

	require.NoError(t, err)
	require.Equal(t, "production", cfg.Environment)
}

// A rate outside [0,1] is a configuration error, not a value to clamp.
func TestLoadSentry_RejectsAnUnusableSampleRate(t *testing.T) {
	cases := map[string]struct {
		sampleRate       string
		tracesSampleRate string
		errContains      string
	}{
		"negative event rate": {
			sampleRate:       "-0.1",
			tracesSampleRate: "0",
			errContains:      "SENTRY_SAMPLE_RATE must be between",
		},
		"event rate above one": {
			sampleRate:       "1.1",
			tracesSampleRate: "0",
			errContains:      "SENTRY_SAMPLE_RATE must be between",
		},
		"event rate that is not a number": {
			sampleRate:       "half",
			tracesSampleRate: "0",
			errContains:      "SENTRY_SAMPLE_RATE must be a number",
		},
		"trace rate above one": {
			sampleRate:       "1",
			tracesSampleRate: "2",
			errContains:      "SENTRY_TRACES_SAMPLE_RATE must be between",
		},
		"trace rate that is not a number": {
			sampleRate:       "1",
			tracesSampleRate: "all",
			errContains:      "SENTRY_TRACES_SAMPLE_RATE must be a number",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadSentryWith(
				t,
				"development",
				"https://key@example.ingest.sentry.io/1",
				"",
				"",
				tc.sampleRate,
				tc.tracesSampleRate,
				"",
			)

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errContains)
		})
	}
}

// A debug flag that is not a boolean is reported rather than silently treated as
// false.
func TestLoadSentry_RejectsAnUnusableDebugFlag(t *testing.T) {
	_, err := loadSentryWith(
		t,
		"development",
		"https://key@example.ingest.sentry.io/1",
		"",
		"",
		"1",
		"0",
		"yes-please",
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "SENTRY_DEBUG must be true or false")
}
