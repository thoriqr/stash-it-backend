package password_reset

import "time"

const (
	passwordResetExpiresIn    					= 15 * time.Minute
	verificationCodeExpiresIn 					= 5 * time.Minute
	passwordResetContinuationExpiresIn 	= 15 * time.Minute
	verificationResendCooldown        	= 60 * time.Second
	VerificationCodeMaxAttempts int32 	= 5
)