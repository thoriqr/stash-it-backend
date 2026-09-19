package password_reset

import "time"

const (
	passwordResetExpiresIn    = 15 * time.Minute
	verificationCodeExpiresIn = 5 * time.Minute
)