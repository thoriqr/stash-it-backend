package password_reset

import "github.com/thoriqr/stash-it-backend/internal/email"

func newVerificationPINEmail(
	recipient string,
	pin string,
) email.Message {
	return email.Message{
		To: email.Recipient{
			Email: recipient,
		},
		Subject: "Your password reset verification code",
		Text:    "Your password reset verification code is: " + pin,
	}
}