package registration

const (
	CodeRegistrationAlreadyPending       = "REGISTRATION_ALREADY_PENDING"
	CodeRegistrationAlreadyCompleted     = "REGISTRATION_ALREADY_COMPLETED"
	CodeRegistrationAlreadyExists        = "REGISTRATION_ALREADY_EXISTS"
	CodeActiveVerificationCodeExists     = "ACTIVE_VERIFICATION_CODE_EXISTS"
	CodeVerificationNotPending           = "VERIFICATION_NOT_PENDING"
	CodeRegistrationNotPending           = "REGISTRATION_NOT_PENDING"
	CodeRegistrationExpired              = "REGISTRATION_EXPIRED"
	CodeRegistrationContinuationConsumed = "REGISTRATION_CONTINUATION_CONSUMED"
	CodeRegistrationContinuationExpired  = "REGISTRATION_CONTINUATION_EXPIRED"
	CodeVerificationResendCooldown       = "VERIFICATION_RESEND_COOLDOWN"
	CodeInvalidVerificationCode          = "INVALID_VERIFICATION_CODE"
	CodeVerificationCodeAttemptsExceeded = "VERIFICATION_CODE_ATTEMPTS_EXCEEDED"
	CodeInvalidRegistrationType          = "INVALID_REGISTRATION_TYPE"
	CodeRegistrationContinuationRequired = "REGISTRATION_CONTINUATION_REQUIRED"
	CodeUserAlreadyExists                = "USER_ALREADY_EXISTS"
	CodeAuthIdentityAlreadyExists        = "AUTH_IDENTITY_ALREADY_EXISTS"
)