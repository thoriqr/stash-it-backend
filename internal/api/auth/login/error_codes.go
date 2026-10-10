package login

const (
	CodeInvalidCredentials             = "INVALID_CREDENTIALS"
	CodeAuthIdentityAlreadyExists      = "AUTH_IDENTITY_ALREADY_EXISTS"
	CodeInvalidGoogleToken             = "INVALID_GOOGLE_TOKEN"
	CodeAccountLinkConfirmationInvalid = "ACCOUNT_LINK_CONFIRMATION_INVALID"

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
)
