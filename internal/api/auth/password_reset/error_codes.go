package password_reset

const (
	CodePasswordResetAlreadyPending  = "PASSWORD_RESET_ALREADY_PENDING"
	CodeActiveVerificationCodeExists = "ACTIVE_VERIFICATION_CODE_EXISTS"

	CodeVerificationNotPending     = "VERIFICATION_NOT_PENDING"
	CodePasswordResetNotPending    = "PASSWORD_RESET_NOT_PENDING"
	CodePasswordResetExpired       = "PASSWORD_RESET_EXPIRED"
	CodeVerificationResendCooldown = "VERIFICATION_RESEND_COOLDOWN"

	CodeInvalidVerificationCode           = "INVALID_VERIFICATION_CODE"
	CodeVerificationCodeAttemptsExceeded  = "VERIFICATION_CODE_ATTEMPTS_EXCEEDED"
	CodePasswordResetContinuationConsumed = "PASSWORD_RESET_CONTINUATION_CONSUMED"
	CodePasswordResetContinuationRequired = "PASSWORD_RESET_CONTINUATION_REQUIRED"
	CodePasswordResetContinuationExpired  = "PASSWORD_RESET_CONTINUATION_EXPIRED"

	// CodePasswordWorkUnavailable reports that the request was refused because
	// every slot for expensive password work was occupied, and not because of
	// anything about the password or the continuation.
	//
	// It is deliberately neither an authentication failure nor a rate-limit code.
	// The password was never hashed, so reporting this as a bad password would be
	// a lie the caller would act on, and reporting it as a rate limit would name a
	// budget that was not spent and a window that does not exist.
	//
	// It carries a Retry-After, because unlike the cases above the recovery time
	// is known: it is the bounded wait the caller already spent, which the shared
	// hasher exposes rather than a constant repeated here.
	//
	// The value matches the login and registration features'. They are separate
	// constants in separate packages because each feature names its own failures,
	// and sharing one declaration would let a change to either silently change
	// both.
	CodePasswordWorkUnavailable = "PASSWORD_WORK_UNAVAILABLE"
)
