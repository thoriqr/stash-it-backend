package email

import (
	"context"
	"errors"
)

type UnconfiguredSender struct{}

func NewUnconfiguredSender() *UnconfiguredSender {
	return &UnconfiguredSender{}
}

// Send refuses, and says it never tried.
//
// The distinction is the reason this is not simply `errors.New`. Every
// non-development build is wired with this sender, so every PIN email it is
// asked to deliver fails. If that failure looked like a failed delivery, each
// attempt would consume a slot of the deployment-wide email budget while
// delivering nothing, and a staging deployment would exhaust the budget that is
// meant to protect a real provider account before anyone had chosen a provider.
//
// Marking it NotAttempted is an accurate description rather than a convenience:
// this sender makes no network call, so there is nothing that could have been
// accepted anywhere.
func (UnconfiguredSender) Send(
	ctx context.Context,
	message Message,
) error {
	return NotAttempted(errors.New("email sender is not configured"))
}
