package password_reset

type VerificationSubjectType string

const (
	VerificationSubjectPendingPasswordReset VerificationSubjectType = "pending_password_reset"
)

type VerificationPurpose string

const (
	VerificationPurposePasswordReset VerificationPurpose = "password_reset"
)

type VerificationRequestStatus string

const (
	VerificationRequestPending  VerificationRequestStatus = "pending"
	VerificationRequestVerified VerificationRequestStatus = "verified"
)

type PendingPasswordResetStatus string

const (
	PendingPasswordResetPending   PendingPasswordResetStatus = "pending"
	PendingPasswordResetCompleted PendingPasswordResetStatus = "completed"
	PendingPasswordResetExpired   PendingPasswordResetStatus = "expired"
)