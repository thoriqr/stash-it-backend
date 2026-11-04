package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The web client remains the one Google audience every deployment must name.
// Without it the verifier would have no audience to validate against, so this
// stays a startup failure rather than an optional setting.
func TestLoad_GoogleClientIDIsRequired(t *testing.T) {
	withLoadableEnv(t)
	t.Setenv("GOOGLE_CLIENT_ID", "")

	cfg, err := Load()

	require.Error(t, err)
	require.Equal(t, Config{}, cfg)
	require.Contains(t, err.Error(), "GOOGLE_CLIENT_ID is required")
}

// A deployment that has not configured a desktop client is ordinary: the
// existing web and mobile flows continue to work, and nothing is invented to
// stand in for the missing audience.
func TestLoad_GoogleDesktopClientIDIsOptional(t *testing.T) {
	withLoadableEnv(t)
	t.Setenv("GOOGLE_DESKTOP_CLIENT_ID", "")

	cfg, err := Load()

	require.NoError(t, err)
	require.Equal(t, "client", cfg.GoogleClientID)
	require.Empty(t, cfg.GoogleDesktopClientID)
}

// When supplied, the desktop client is loaded unchanged so the verifier can use
// it as an additional explicit audience.
func TestLoad_GoogleDesktopClientIDSurvivesIntoTheConfig(t *testing.T) {
	withLoadableEnv(t)
	t.Setenv("GOOGLE_DESKTOP_CLIENT_ID", "desktop-client")

	cfg, err := Load()

	require.NoError(t, err)
	require.Equal(t, "client", cfg.GoogleClientID)
	require.Equal(t, "desktop-client", cfg.GoogleDesktopClientID)
}
