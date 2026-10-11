package login

const (
	CodeInvalidCredentials             = "INVALID_CREDENTIALS"
	CodeAuthIdentityAlreadyExists      = "AUTH_IDENTITY_ALREADY_EXISTS"
	CodeInvalidGoogleToken             = "INVALID_GOOGLE_TOKEN"
	CodeAccountLinkConfirmationInvalid = "ACCOUNT_LINK_CONFIRMATION_INVALID"

	// CodeAccountLinkIdentityMismatch reports that the Google identity the caller
	// proved control of is not the one the confirmation was created for.
	//
	// It is deliberately distinct from CodeInvalidGoogleToken even though both are
	// refusals, because the two ask the caller to do opposite things. An invalid
	// token means the credential itself is unusable and a fresh one must be
	// obtained; a mismatch means the credential is perfectly valid and simply
	// belongs to a different account, which no amount of retrying will fix. A
	// caller that cannot tell them apart would sit in a retry loop.
	//
	// The message names neither the stored identity nor the target account, so a
	// mismatched caller learns only that the two do not correspond.
	CodeAccountLinkIdentityMismatch = "ACCOUNT_LINK_IDENTITY_MISMATCH"

	// CodeAccountLinkSessionFailed reports that the identity was linked and the
	// confirmation consumed, and the session could not then be created.
	//
	// It exists because the alternative is a lie the client would act on. The
	// confirmation is single-use, so retrying this endpoint can only ever return
	// ACCOUNT_LINK_CONFIRMATION_INVALID, which would read as "your confirmation
	// was wrong" when in fact the link succeeded and only the session is missing.
	// Retrying Google login does recover, because the identity is now linked and
	// that route authenticates it directly.
	//
	// It is a 503 rather than a fault because it is transient by construction:
	// nothing about the request is wrong and the same call cannot succeed on a
	// retry. It carries no Retry-After for the same reason — recovery does not
	// wait out a window, it waits out nothing at all.
	CodeAccountLinkSessionFailed = "ACCOUNT_LINK_SESSION_FAILED"

	// CodeLoginRateLimitExceeded reports that a budget guarding this endpoint is
	// spent. It does not say which one: naming the dimension would tell a caller
	// whether their guess ran into a shared address or into a specific account,
	// which is more than the response needs to carry and more than the path it
	// already reveals.
	CodeLoginRateLimitExceeded = "LOGIN_RATE_LIMIT_EXCEEDED"

	// CodeLoginRateLimitUnavailable reports that a budget could not be
	// evaluated, so authentication was refused without ever being attempted.
	//
	// It is distinct from CodeLoginRateLimitExceeded because the two ask the
	// caller to do opposite things: one means they asked for too much and should
	// wait out a window, the other means the server could not tell and their
	// credentials were never tried at all.
	CodeLoginRateLimitUnavailable = "LOGIN_RATE_LIMIT_UNAVAILABLE"

	// CodePasswordWorkUnavailable reports that the request was refused because
	// every slot for expensive password work was occupied, not because of
	// anything about the credentials.
	//
	// It is deliberately neither CodeInvalidCredentials nor a rate-limit code. The
	// password was never checked, so reporting it as a bad password would be a
	// lie the caller would act on, and reporting it as a rate limit would name a
	// budget that was not spent and a window that does not exist.
	//
	// It carries a Retry-After, because unlike the cases above the recovery time
	// is known: it is the bounded wait the caller already spent.
	CodePasswordWorkUnavailable = "PASSWORD_WORK_UNAVAILABLE"
)
