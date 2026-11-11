package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The provider settings: the default for each environment, the configuration a
// Brevo deployment must supply, and every way a supplied value can be wrong.
//
// These go through loadEmailSender rather than the exported loader because the
// rest of Load() needs a whole set of unrelated required values, and a case about
// outbound mail is not a case about those.

func loadEmailSenderWith(
	t *testing.T,
	appEnv string,
	provider string,
	apiKey string,
	fromAddress string,
	fromName string,
	timeoutSeconds string,
) (EmailSenderConfig, error) {
	t.Helper()

	t.Setenv("EMAIL_PROVIDER", provider)
	t.Setenv("BREVO_API_KEY", apiKey)
	t.Setenv("EMAIL_FROM_ADDRESS", fromAddress)
	t.Setenv("EMAIL_FROM_NAME", fromName)
	t.Setenv("EMAIL_TIMEOUT_SECONDS", timeoutSeconds)

	return loadEmailSender(appEnv)
}

// An unset provider keeps the behaviour each environment already had: development
// logs messages, everything else refuses them. The refusal is what keeps a
// staging deployment from spending a real provider account's quota.
func TestLoadEmailSender_DefaultsByEnvironment(t *testing.T) {
	development, err := loadEmailSenderWith(
		t, "development", "", "", "", "", "",
	)
	require.NoError(t, err)
	require.Equal(t, EmailProviderDev, development.Provider)

	nonDevelopment, err := loadEmailSenderWith(
		t, "production", "", "", "", "", "",
	)
	require.NoError(t, err)
	require.Equal(t, EmailProviderNone, nonDevelopment.Provider)
}

// A named provider that needs no credentials is accepted as written.
func TestLoadEmailSender_AcceptsProvidersWithoutCredentials(t *testing.T) {
	dev, err := loadEmailSenderWith(t, "production", "dev", "", "", "", "")
	require.NoError(t, err)
	require.Equal(t, EmailProviderDev, dev.Provider)

	none, err := loadEmailSenderWith(t, "production", "none", "", "", "", "")
	require.NoError(t, err)
	require.Equal(t, EmailProviderNone, none.Provider)
}

// A complete Brevo configuration resolves to the values the sender is built from.
func TestLoadEmailSender_AcceptsACompleteBrevoConfiguration(t *testing.T) {
	cfg, err := loadEmailSenderWith(
		t,
		"production",
		"brevo",
		"  brevo-key  ",
		"  no-reply@example.com  ",
		"  Stash It  ",
		"5",
	)

	require.NoError(t, err)
	require.Equal(t, EmailProviderBrevo, cfg.Provider)
	require.Equal(t, "brevo-key", cfg.BrevoAPIKey)
	require.Equal(t, "no-reply@example.com", cfg.FromAddress)
	require.Equal(t, "Stash It", cfg.FromName)
	require.Equal(t, 5*time.Second, cfg.Timeout)
}

// An absent timeout resolves to the default rather than to a zero-duration client
// that would time out every send.
func TestLoadEmailSender_DefaultsTheBrevoTimeout(t *testing.T) {
	cfg, err := loadEmailSenderWith(
		t,
		"production",
		"brevo",
		"brevo-key",
		"no-reply@example.com",
		"",
		"",
	)

	require.NoError(t, err)
	require.Equal(t, DefaultEmailTimeoutSeconds*time.Second, cfg.Timeout)
}

// A provider that needs credentials is refused when they are missing. Finding
// that at startup is much cheaper than finding it as a registration flow that
// fails for every user.
func TestLoadEmailSender_RejectsAnIncompleteBrevoConfiguration(t *testing.T) {
	cases := map[string]struct {
		apiKey      string
		fromAddress string
		errContains string
	}{
		"missing api key": {
			apiKey:      "",
			fromAddress: "no-reply@example.com",
			errContains: "BREVO_API_KEY is required",
		},
		"missing sender address": {
			apiKey:      "brevo-key",
			fromAddress: "",
			errContains: "EMAIL_FROM_ADDRESS is required",
		},
		"invalid sender address": {
			apiKey:      "brevo-key",
			fromAddress: "not-an-address",
			errContains: "must be a valid email address",
		},
		"display-name sender address": {
			apiKey:      "brevo-key",
			fromAddress: "Stash It <no-reply@example.com>",
			errContains: "must be a bare email address",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadEmailSenderWith(
				t,
				"production",
				"brevo",
				tc.apiKey,
				tc.fromAddress,
				"",
				"",
			)

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errContains)
		})
	}
}

// A provider this application cannot build is a mistake, not something to
// silently replace with another.
func TestLoadEmailSender_RejectsAnUnknownProvider(t *testing.T) {
	_, err := loadEmailSenderWith(
		t, "production", "smtp", "", "", "", "",
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "EMAIL_PROVIDER")
	require.Contains(t, err.Error(), "smtp")
}

// The timeout is range-checked for the same reason every other bounded setting
// is: a typo must not become a client that gives up immediately or holds a
// request open far longer than the response is worth.
func TestLoadEmailSender_RejectsAnUnusableTimeout(t *testing.T) {
	cases := map[string]struct {
		timeout     string
		errContains string
	}{
		"zero": {
			timeout:     "0",
			errContains: "EMAIL_TIMEOUT_SECONDS must be between",
		},
		"past the ceiling": {
			timeout:     "31",
			errContains: "EMAIL_TIMEOUT_SECONDS must be between",
		},
		"not a number": {
			timeout:     "ten",
			errContains: "EMAIL_TIMEOUT_SECONDS must be a whole number",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadEmailSenderWith(
				t,
				"production",
				"brevo",
				"brevo-key",
				"no-reply@example.com",
				"",
				tc.timeout,
			)

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errContains)
		})
	}
}
