package session

const (
	CodeRefreshTokenInvalid   = "REFRESH_TOKEN_INVALID"
	CodeSessionRevoked        = "SESSION_REVOKED"
	CodeSessionExpired        = "SESSION_EXPIRED"
	CodeInvalidInstallationID = "INVALID_INSTALLATION_ID"

	// CodeCleanupSecretInvalid is reported by the internal maintenance guard.
	//
	// One code covers every way of failing it: a missing header, an empty header,
	// a wrong value, and a deployment that configured no secret at all. They are
	// deliberately indistinguishable. A caller who can tell "wrong secret" from
	// "no secret configured" learns whether this deployment runs the cleanup,
	// which is not something an unauthenticated caller should be able to find out.
	CodeCleanupSecretInvalid = "CLEANUP_SECRET_INVALID"
)