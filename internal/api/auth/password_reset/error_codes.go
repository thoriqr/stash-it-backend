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

	// CodePinRateLimitExceeded reports that the shared per-address PIN budget is
	// spent.
	//
	// It is the code registration has always used for the same budget, and it is
	// deliberately the same string rather than a feature-specific one: there is one
	// budget, so there is one answer to "this address may not be mailed any more".
	CodePinRateLimitExceeded = "PIN_RATE_LIMIT_EXCEEDED"

	// CodePinRateLimitUnavailable reports that the per-address PIN budget could
	// not be evaluated, so no code was issued and no message was sent.
	CodePinRateLimitUnavailable = "PIN_RATE_LIMIT_UNAVAILABLE"

	// CodePINEmailBudgetExceeded reports that the deployment-wide PIN email
	// budget is spent.
	//
	// It is distinct from CodePinRateLimitExceeded even though both are 429s. One
	// says this address may not be mailed; the other says the deployment may not
	// mail anybody, which is an operational condition and not a client's mistake.
	CodePINEmailBudgetExceeded = "PIN_EMAIL_BUDGET_EXCEEDED"

	// CodePINEmailBudgetUnavailable reports that the deployment-wide budget could
	// not be evaluated.
	CodePINEmailBudgetUnavailable = "PIN_EMAIL_BUDGET_UNAVAILABLE"

	// CodePINEmailDeliveryFailed reports that the PIN was persisted but the
	// message could not be handed to the sender.
	//
	// It is a 503 carrying a Retry-After rather than the 500 this path used to
	// return. The reset is still open, the verification is still pending and
	// retrying is exactly the right response — which is what a 500 tells a client
	// not to do — and the wait is knowable, because persisting the code restarted
	// the resend cooldown.
	//
	// It names delivery rather than the sender on purpose. Whether the message was
	// rejected, deferred or simply lost is not something this application knows,
	// and a code claiming otherwise would be a claim it cannot support.
	CodePINEmailDeliveryFailed = "PIN_EMAIL_DELIVERY_FAILED"

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
