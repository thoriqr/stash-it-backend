package config

import (
	"fmt"
	"net/mail"
	"os"
	"strings"
	"time"
)

// EmailProvider selects how outbound mail leaves the process.
//
// It is explicit rather than inferred from which credentials happen to be
// present, because "this deployment sends mail" is a decision an operator makes
// and a typo in a credential name must not silently turn delivery off or on.
type EmailProvider string

const (
	// EmailProviderDev logs messages instead of delivering them. It is the
	// default for a development build and never makes a network call.
	EmailProviderDev EmailProvider = "dev"

	// EmailProviderBrevo delivers through Brevo's transactional email API.
	EmailProviderBrevo EmailProvider = "brevo"

	// EmailProviderNone refuses every send and reports that no attempt was made.
	// It is the default for a non-development build that has not chosen a
	// provider yet, so a staging deployment cannot drain a production account's
	// quota by accident.
	EmailProviderNone EmailProvider = "none"
)

// Email timeout bounds, in seconds.
//
// The ceiling is deliberately small: mail is sent while a client waits for the
// PIN response, and a provider that has not answered within this window has
// already failed from the caller's point of view.
const (
	DefaultEmailTimeoutSeconds = 10
	MinEmailTimeoutSeconds     = 1
	MaxEmailTimeoutSeconds     = 30
)

// EmailSenderConfig is the resolved outbound-mail configuration.
type EmailSenderConfig struct {
	Provider EmailProvider

	// BrevoAPIKey is the credential for the Brevo API. It is a secret and must
	// never be logged.
	BrevoAPIKey string

	// FromAddress and FromName are the sender identity. The address must be a
	// verified sender or belong to a domain authenticated in Brevo.
	FromAddress string
	FromName    string

	// Timeout bounds one send attempt, so a slow provider cannot hold the
	// request that produced the PIN open indefinitely.
	Timeout time.Duration
}

// loadEmailSender reads and validates the outbound-mail configuration.
//
// An unset EMAIL_PROVIDER keeps the behaviour each environment already had:
// development logs messages, everything else refuses them. A provider that is
// named but incomplete is an error at startup, because a deployment that cannot
// send the PINs it issues would otherwise be discovered by the first person who
// tries to register.
func loadEmailSender(appEnv string) (EmailSenderConfig, error) {
	provider := EmailProvider(strings.TrimSpace(os.Getenv("EMAIL_PROVIDER")))

	if provider == "" {
		if appEnv == "development" {
			provider = EmailProviderDev
		} else {
			provider = EmailProviderNone
		}
	}

	switch provider {
	case EmailProviderDev, EmailProviderNone:
		return EmailSenderConfig{Provider: provider}, nil

	case EmailProviderBrevo:
		apiKey := strings.TrimSpace(os.Getenv("BREVO_API_KEY"))
		if apiKey == "" {
			return EmailSenderConfig{}, fmt.Errorf(
				"BREVO_API_KEY is required when EMAIL_PROVIDER=brevo",
			)
		}

		fromAddress, err := loadEmailFromAddress()
		if err != nil {
			return EmailSenderConfig{}, err
		}

		timeoutSeconds, err := loadBoundedInt(
			"EMAIL_TIMEOUT_SECONDS",
			DefaultEmailTimeoutSeconds,
			MinEmailTimeoutSeconds,
			MaxEmailTimeoutSeconds,
		)
		if err != nil {
			return EmailSenderConfig{}, err
		}

		return EmailSenderConfig{
			Provider:    provider,
			BrevoAPIKey: apiKey,
			FromAddress: fromAddress,
			FromName:    strings.TrimSpace(os.Getenv("EMAIL_FROM_NAME")),
			Timeout:     time.Duration(timeoutSeconds) * time.Second,
		}, nil

	default:
		return EmailSenderConfig{}, fmt.Errorf(
			"EMAIL_PROVIDER must be one of %q, %q or %q, got %q",
			EmailProviderDev,
			EmailProviderBrevo,
			EmailProviderNone,
			provider,
		)
	}
}

// loadEmailFromAddress requires a bare address rather than a display-name form.
//
// The display name has its own variable, and accepting "Name <address>" here
// would make the address and the name disagree depending on which one the
// operator filled in. The parsed address is returned so the value used by the
// sender is the one the parser accepted.
func loadEmailFromAddress() (string, error) {
	raw := strings.TrimSpace(os.Getenv("EMAIL_FROM_ADDRESS"))
	if raw == "" {
		return "", fmt.Errorf(
			"EMAIL_FROM_ADDRESS is required when EMAIL_PROVIDER=brevo",
		)
	}

	parsed, err := mail.ParseAddress(raw)
	if err != nil {
		return "", fmt.Errorf(
			"EMAIL_FROM_ADDRESS must be a valid email address: %w",
			err,
		)
	}

	// The display name has its own variable. Checking the parsed name rather than
	// comparing the raw string also keeps a differently-cased domain from being
	// rejected as if it had a display name.
	if parsed.Name != "" {
		return "", fmt.Errorf(
			"EMAIL_FROM_ADDRESS must be a bare email address, " +
				"without a display name; use EMAIL_FROM_NAME for that",
		)
	}

	return parsed.Address, nil
}
