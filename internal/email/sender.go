package email

import "context"

type Sender interface {
	Send(ctx context.Context, message Message) error
}

type Message struct {
	To Recipient
	Subject string
	HTML string
	Text string
}

type Recipient struct {
	Email string
	Name  string
}