package email

import "context"

// Sender delivers one message.
//
// A sender that cannot reach a provider at all should return an error marked by
// NotAttempted, so the caller can tell "nothing was sent" from "something was
// sent and the outcome is unknown" without the sender having to explain provider
// failure modes to its caller. See ErrNotAttempted in budget.go.
type Sender interface {
	Send(ctx context.Context, message Message) error
}

type Message struct {
	To Recipient
	Subject string
	HTML    string
	Text    string
}

type Recipient struct {
	Email string
	Name  string
}
