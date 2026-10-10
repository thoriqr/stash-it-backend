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
	CodePinRateLimitExceeded             = "PIN_RATE_LIMIT_EXCEEDED"
	CodePinRateLimitUnavailable          = "PIN_RATE_LIMIT_UNAVAILABLE"

	// CodePasswordWorkUnavailable reports that the request was refused because
	// every slot for expensive password work was occupied.
	//
	// It is not a rate-limit code: no budget was spent and there is no window
	// involved. It carries a Retry-After because the recovery time is known — it
	// is the bounded wait the caller already spent — and because a caller told
	// only that the server is busy has no way to decide when to come back.
	//
	// The value matches the login feature's. They are separate constants in
	// separate packages because each feature names its own failures, and sharing
	// one declaration would let a change to either silently change both.
	CodePasswordWorkUnavailable = "PASSWORD_WORK_UNAVAILABLE"
)
