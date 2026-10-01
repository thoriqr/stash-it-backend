package password_reset

import "github.com/google/uuid"

type RequestPasswordResetResponse struct {
    VerificationID uuid.UUID `json:"verification_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
}

type GetVerificationResponse struct {
    VerificationID        string `json:"verification_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
    Status                string `json:"status" example:"pending"`
    PINIssued             bool   `json:"pin_issued" example:"true"`
    ResendInSeconds       int    `json:"resend_in_seconds" example:"42"`
}

type ResendVerificationResponse struct {
    VerificationID string `json:"verification_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
}

type VerifyPasswordResetResponse struct {
    PasswordResetContinuationToken string `json:"password_reset_continuation_token" example:"eyJhbGciOiJIUzI1NiJ9..."`
}

type GetPasswordResetContinuationResponse struct {
    Email                 string `json:"email" example:"user@example.com"`
    HasPasswordCredential bool   `json:"has_password_credential" example:"true"`
}

type FinalizeManualRegistrationResponse struct {
    Email       string `json:"email"`
    DisplayName string `json:"display_name"`
}

type CreatePINResponse struct {
    VerificationID string `json:"verification_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
}

type RequestPasswordResetAPIResponse struct {
    Data    *RequestPasswordResetResponse `json:"data"`
    Message string                        `json:"message" example:"password reset requested"`
}

type CreatePINAPIResponse struct {
    Data    *CreatePINResponse `json:"data"`
    Message string             `json:"message" example:"verification code created"`
}

type GetVerificationAPIResponse struct {
    Data    *GetVerificationResponse `json:"data"`
    Message string                   `json:"message" example:"verification retrieved"`
}

type ResendVerificationAPIResponse struct {
    Data    *ResendVerificationResponse `json:"data"`
    Message string                      `json:"message" example:"verification code resent"`
}

type VerifyPasswordResetAPIResponse struct {
    Data    *VerifyPasswordResetResponse `json:"data"`
    Message string                       `json:"message" example:"password reset verified"`
}

type GetPasswordResetContinuationAPIResponse struct {
    Data    *GetPasswordResetContinuationResponse `json:"data"`
    Message string                                `json:"message" example:"password reset continuation is valid"`
}

type FinalizePasswordResetAPIResponse struct {
    Data    *struct{} `json:"data"`
    Message string    `json:"message" example:"password reset completed successfully"`
}