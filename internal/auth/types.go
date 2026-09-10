package auth

type VerificationSubjectType string

const (
	VerificationSubjectPendingRegistration VerificationSubjectType = "pending_registration"
)

type VerificationPurpose string

const (
	VerificationPurposeRegistration VerificationPurpose = "registration"
)

type VerificationRequestStatus string

const (
	VerificationRequestPending  VerificationRequestStatus = "pending"
	VerificationRequestVerified VerificationRequestStatus = "verified"
)

type RegistrationType string

const (
	RegistrationTypeManual RegistrationType = "manual"
	RegistrationTypeSocial RegistrationType = "social"
)

type PendingRegistrationStatus string

const (
	PendingRegistrationPending   PendingRegistrationStatus = "pending"
	PendingRegistrationCompleted PendingRegistrationStatus = "completed"
	PendingRegistrationExpired   PendingRegistrationStatus = "expired"
)