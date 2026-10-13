package email

import (
	"context"

	"go.uber.org/zap"
)

type DevSender struct {
	logger *zap.Logger

	// Err, when set, is returned instead of delivering. It exists so a test can
	// stand up a sender that behaves like one which reached a provider and failed,
	// which is the case the budget policy treats differently from one that never
	// tried.
	Err error
}

func NewDevSender(logger *zap.Logger) *DevSender {
	return &DevSender{
		logger: logger,
	}
}

// Send logs the message instead of delivering it, and returns Err if one is set.
//
// Its budget behaviour is deliberate and worth stating, because the sender is
// wired into every development and test build and therefore spends real budget
// counters against a real Redis whenever Redis is present.
//
// A successful call consumes a slot and does not get one back, because the
// message was "delivered" as far as this sender is concerned. A failing call also
// consumes its slot, and deliberately so: this sender stands in for a provider
// that was reached, so its failures are attempted failures. That makes it the
// opposite of UnconfiguredSender, which reports NotAttempted, and the two together
// are what lets a test prove the budget refunds one and not the other.
//
// The logged fields include the recipient and the body, which for a PIN message
// contains the PIN. That is acceptable at Debug in development and is exactly the
// shape a production sender must not inherit: see the same note in the deployment
// documentation for this feature.
func (s *DevSender) Send(
	ctx context.Context,
	message Message,
) error {
	if s.Err != nil {
		return s.Err
	}

	s.logger.Debug(
		"DEV email generated",
		zap.String("email", message.To.Email),
		zap.String("subject", message.Subject),
		zap.String("text", message.Text),
	)

	return nil
}
