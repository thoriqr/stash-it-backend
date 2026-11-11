package main

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/email"
)

// newEmailSender resolves the configured provider into a sender.
//
// A Brevo deployment is checked once here rather than at the first registration.
// A wrong API key or a source address Brevo has not authorized is a permanent
// deployment mistake, so it stops the process instead of becoming a flow that
// fails for every user. Every other failure is only logged: Brevo being
// unreachable must not take down endpoints that do not send mail.
func newEmailSender(
	ctx context.Context,
	cfg config.Config,
	log *zap.Logger,
) (email.Sender, error) {
	switch cfg.EmailSender.Provider {
	case config.EmailProviderDev:
		return email.NewDevSender(log), nil

	case config.EmailProviderNone:
		log.Warn(
			"email provider is not configured; every PIN delivery will fail",
		)

		return email.NewUnconfiguredSender(), nil

	case config.EmailProviderBrevo:
		sender := email.NewBrevoSender(
			log,
			cfg.EmailSender.BrevoAPIKey,
			email.DefaultBrevoAPIURL,
			cfg.EmailSender.FromAddress,
			cfg.EmailSender.FromName,
			cfg.EmailSender.Timeout,
		)

		if err := sender.Check(ctx); err != nil {
			if errors.Is(err, email.ErrBrevoUnauthorized) {
				return nil, err
			}

			log.Warn(
				"brevo is unreachable at startup; continuing",
				zap.Error(err),
			)
		}

		return sender, nil

	default:
		return nil, fmt.Errorf(
			"unsupported email provider %q",
			cfg.EmailSender.Provider,
		)
	}
}
