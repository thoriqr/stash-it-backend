package password_reset

import "github.com/google/uuid"

type RequestPasswordResetResponse struct {
	VerificationID uuid.UUID `json:"verification_id"`
}

type GetVerificationResponse struct {
	VerificationID   string `json:"verification_id"`
	Status           string `json:"status"`
	ResendInSeconds  int    `json:"resend_in_seconds"`
}

type ResendVerificationResponse struct {
	VerificationID string `json:"verification_id"`
}

type VerifyPasswordResetResponse struct {
	PasswordResetContinuationToken string `json:"password_reset_continuation_token"`
}

type GetPasswordResetContinuationResponse struct {
	Email            string `json:"email"`
	HasPasswordCredential bool   `json:"has_password_credential"`
}

type FinalizeManualRegistrationResponse struct {
    Email       string `json:"email"`
    DisplayName string `json:"display_name"`
}