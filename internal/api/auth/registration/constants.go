package registration

import "time"

const (
	registrationExpiresIn             = 7 * 24 * time.Hour
	registrationContinuationExpiresIn = 15 * time.Minute
	verificationCodeExpiresIn         = 5 * time.Minute
	verificationResendCooldown        = 60 * time.Second
	VerificationCodeMaxAttempts      int32 = 5
)