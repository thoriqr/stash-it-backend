package registration

import "github.com/thoriqr/stash-it-backend/internal/email"

func newVerificationPINEmail(
	recipient string,
	pin string,
) email.Message {
	return email.Message{
		To: email.Recipient{
			Email: recipient,
		},
		Subject: "Your registration verification code",
		Text:    "Your registration verification code is: " + pin,
	}
}