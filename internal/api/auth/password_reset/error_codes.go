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
)