package registration

import "github.com/google/uuid"

type RegisterResponse struct {
	VerificationID uuid.UUID `json:"verification_id"`
}

type CreatePINResponse struct {
	VerificationID string `json:"verification_id"`
}

type GetVerificationResponse struct {
    VerificationID  string `json:"verification_id"`
    Status          string `json:"status"`
    PINIssued       bool   `json:"pin_issued"`
    ResendInSeconds  int    `json:"resend_in_seconds"`
}

type ResendVerificationResponse struct {
	VerificationID string `json:"verification_id"`
}

type VerifyRegistrationResponse struct {
	RegistrationContinuationToken string `json:"registration_continuation_token"`
}

type GetRegistrationContinuationResponse struct {
	Email            string `json:"email"`
	RegistrationType string `json:"registration_type"`
	RequiresPassword bool   `json:"requires_password"`
}

type FinalizeManualRegistrationResponse struct {
    Email       string `json:"email"`
    DisplayName string `json:"display_name"`
}

