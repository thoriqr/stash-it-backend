package email

import (
	"context"

	"go.uber.org/zap"
)

type DevSender struct {
	logger *zap.Logger
}

func NewDevSender(logger *zap.Logger) *DevSender {
	return &DevSender{
		logger: logger,
	}
}

func (s *DevSender) Send(
	ctx context.Context,
	message Message,
) error {
	s.logger.Debug(
		"DEV email generated",
		zap.String("email", message.To.Email),
		zap.String("subject", message.Subject),
		zap.String("text", message.Text),
	)

	return nil
}