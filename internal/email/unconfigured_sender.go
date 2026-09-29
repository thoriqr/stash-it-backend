package email

import (
	"context"
	"errors"
)

type UnconfiguredSender struct{}

func NewUnconfiguredSender() *UnconfiguredSender {
	return &UnconfiguredSender{}
}

func (UnconfiguredSender) Send(
	ctx context.Context,
	message Message,
) error {
	return errors.New("email sender is not configured")
}