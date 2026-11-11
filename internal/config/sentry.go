package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// SentryConfig controls the optional Sentry error reporter.
//
// DSN is deliberately optional. An unset DSN disables the reporter entirely, so
// local development and a deployment that has not created a Sentry project
// behave exactly as they did before: the logger remains the only error sink.
type SentryConfig struct {
	DSN              string
	Environment      string
	Release          string
	SampleRate       float64
	TracesSampleRate float64
	Debug            bool
}

// loadSentry reads the optional Sentry settings.
//
// An empty SENTRY_DSN is an ordinary deployment, not a mistake. Every other
// value, when present, is validated: a sample rate outside [0,1] or a debug flag
// that is not a boolean is a configuration error rather than something silently
// replaced.
func loadSentry(appEnv string) (SentryConfig, error) {
	environment := strings.TrimSpace(os.Getenv("SENTRY_ENVIRONMENT"))
	if environment == "" {
		environment = appEnv
	}

	// A zero sample rate is accepted by the SDK as "send all events", so an
	// empty DSN is the supported way to disable reporting rather than a zero
	// rate here.
	sampleRate, err := loadBoundedFloat(
		"SENTRY_SAMPLE_RATE",
		1.0,
		0,
		1,
	)
	if err != nil {
		return SentryConfig{}, err
	}

	tracesSampleRate, err := loadBoundedFloat(
		"SENTRY_TRACES_SAMPLE_RATE",
		0,
		0,
		1,
	)
	if err != nil {
		return SentryConfig{}, err
	}

	debug, err := loadBool("SENTRY_DEBUG", false)
	if err != nil {
		return SentryConfig{}, err
	}

	return SentryConfig{
		DSN:              strings.TrimSpace(os.Getenv("SENTRY_DSN")),
		Environment:      environment,
		Release:          strings.TrimSpace(os.Getenv("SENTRY_RELEASE")),
		SampleRate:       sampleRate,
		TracesSampleRate: tracesSampleRate,
		Debug:            debug,
	}, nil
}

func loadBoundedFloat(
	name string,
	fallback, minimum, maximum float64,
) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", name, err)
	}

	if value < minimum || value > maximum {
		return 0, fmt.Errorf(
			"%s must be between %g and %g, got %g",
			name,
			minimum,
			maximum,
			value,
		)
	}

	return value, nil
}

func loadBool(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false: %w", name, err)
	}

	return value, nil
}
