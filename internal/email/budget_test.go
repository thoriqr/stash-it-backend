package email_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/email"
)

// The only question the PIN email budget asks about a failed send is whether the
// provider could have been reached at all. These tests pin the answer, because
// getting it wrong in either direction costs money: treating a sender that never
// tried as one that did drains a deployment's allowance on sends that delivered
// nothing, and the reverse hands back capacity a message may already have been
// delivered against.

// A sender with nothing configured makes no network call, so nothing could have
// been accepted anywhere. It says so.
func TestUnconfiguredSender_ReportsThatItNeverTried(t *testing.T) {
	err := email.NewUnconfiguredSender().Send(t.Context(), email.Message{
		To:      email.Recipient{Email: "alice@example.com"},
		Subject: "Your code",
	})

	require.Error(t, err)
	require.False(
		t,
		email.Attempted(err),
		"an unconfigured sender must not be reported as an attempted send",
	)
	require.ErrorIs(t, err, email.ErrNotAttempted)

	// The reason still survives the marker, so a log line keeps saying what went
	// wrong rather than only that something did.
	require.ErrorContains(t, err, "email sender is not configured")
}

// The development sender stands in for a provider that was reached, so its
// failures are attempted failures. That is the opposite of the unconfigured
// sender, and the pair is what lets a test tell the two refund decisions apart.
func TestDevSender_ReportsEveryFailureAsAttempted(t *testing.T) {
	sender := email.NewDevSender(zap.NewNop())

	require.NoError(t, sender.Send(t.Context(), email.Message{
		To:      email.Recipient{Email: "alice@example.com"},
		Subject: "Your code",
	}))

	sender.Err = errors.New("read: connection reset by peer")

	err := sender.Send(t.Context(), email.Message{
		To:      email.Recipient{Email: "alice@example.com"},
		Subject: "Your code",
	})

	require.Error(t, err)
	require.True(
		t,
		email.Attempted(err),
		"a sender standing in for a provider keeps its slot on failure",
	)
}

// A nil error is not a failure and is not asked about. Reporting it as "not
// attempted" would make a successful send look refundable.
func TestAttempted_NilIsNotAskedAbout(t *testing.T) {
	require.False(t, email.Attempted(nil))
}

// Anything not explicitly marked is treated as attempted, which is the safe
// direction: it keeps a budget slot the alternative would have handed back.
func TestAttempted_UnmarkedErrorsAreTreatedAsAttempted(t *testing.T) {
	require.True(t, email.Attempted(errors.New("some provider failure")))
	require.True(
		t,
		email.Attempted(errors.New("wrapped: ")),
		"an error with no marker is assumed to have reached the provider",
	)
}

// The marker composes with an existing error rather than replacing it, so a
// sender that knows both the outcome and the reason can report both.
func TestNotAttempted_KeepsTheUnderlyingError(t *testing.T) {
	underlying := errors.New("no transport configured")

	err := email.NotAttempted(underlying)

	require.ErrorIs(t, err, email.ErrNotAttempted)
	require.ErrorIs(t, err, underlying)
	require.False(t, email.Attempted(err))
}

// The marker is read through the whole error chain, so a sender that wraps its
// real cause several layers deep still reports the outcome accurately. Without
// this, the one piece of information the refund decision turns on would be lost
// to ordinary wrapping.
func TestNotAttempted_IsReadThroughTheErrorChain(t *testing.T) {
	wrapped := fmt.Errorf(
		"sending the verification message: %w",
		email.NotAttempted(errors.New("no transport configured")),
	)

	require.False(
		t,
		email.Attempted(wrapped),
		"a marker buried under wrapping still says the provider was not reached",
	)
	require.ErrorContains(t, wrapped, "no transport configured")

	// And an unmarked error wrapped the same way stays attempted.
	require.True(
		t,
		email.Attempted(fmt.Errorf("sending: %w", errors.New("timeout"))),
	)
}