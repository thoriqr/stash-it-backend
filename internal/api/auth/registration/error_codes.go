package registration

const (
	CodeRegistrationAlreadyPending       = "REGISTRATION_ALREADY_PENDING"
	CodeRegistrationAlreadyCompleted     = "REGISTRATION_ALREADY_COMPLETED"
	CodeRegistrationAlreadyExists        = "REGISTRATION_ALREADY_EXISTS"
	CodeRegistrationIdentityMismatch     = "REGISTRATION_IDENTITY_MISMATCH"
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
	// CodePinRateLimitExceeded reports that the shared per-address PIN budget is
	// spent.
	//
	// It is the same code the per-address budget has always carried, and it is
	// unchanged, because what the budget is has not changed: only its size and
	// window have. Clients already handling it keep working.
	CodePinRateLimitExceeded = "PIN_RATE_LIMIT_EXCEEDED"

	// CodePinRateLimitUnavailable reports that the per-address PIN budget could
	// not be evaluated.
	CodePinRateLimitUnavailable = "PIN_RATE_LIMIT_UNAVAILABLE"

	// CodePINEmailBudgetExceeded reports that the deployment-wide PIN email
	// budget is spent.
	//
	// It is deliberately distinct from CodePinRateLimitExceeded even though both
	// are 429s. One says this address may not be mailed any more; the other says
	// this deployment may not mail anybody, which is an operational condition an
	// operator has to see and a client cannot do anything clever about.
	CodePINEmailBudgetExceeded = "PIN_EMAIL_BUDGET_EXCEEDED"

	// CodePINEmailBudgetUnavailable reports that the deployment-wide budget could
	// not be evaluated, so no PIN was issued and no message was sent.
	CodePINEmailBudgetUnavailable = "PIN_EMAIL_BUDGET_UNAVAILABLE"

	// CodePINEmailDeliveryFailed reports that the PIN was persisted but the
	// message could not be handed to the sender.
	//
	// It is a 503 carrying a Retry-After rather than the 500 this path used to
	// return, for two reasons. The caller's situation is not a broken server: the
	// verification is still pending and retrying is exactly the right response,
	// which is what a 500 tells a client not to do. And the wait is knowable —
	// persisting the code restarted the resend cooldown — so the response can say
	// when to come back instead of leaving the client to guess.
	//
	// It names delivery rather than the sender on purpose. Whether the message was
	// rejected, deferred, or simply lost is not something this application knows,
	// and a code that claimed otherwise would be a claim it cannot support.
	CodePINEmailDeliveryFailed = "PIN_EMAIL_DELIVERY_FAILED"

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
