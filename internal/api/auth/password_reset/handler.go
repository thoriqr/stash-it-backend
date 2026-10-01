package password_reset

import (
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

// RequestPasswordReset godoc
// @Summary Request password reset
// @Description Start a password reset flow using an email address.
// @Description Possible error codes:
// @Description - RESOURCE_NOT_FOUND
// @Description - INTERNAL_SERVER_ERROR
// @Tags Password Reset
// @Accept json
// @Produce json
// @Param request body RequestPasswordResetRequest true "Password reset request"
// @Success 201 {object} RequestPasswordResetAPIResponse
// @Failure 400 {object} swagger.ValidationErrorResponse "Validation error"
// @Failure 404 {object} swagger.APIErrorResponse "User not found"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/password-reset [post]
func (h *Handler) RequestPasswordReset(c fiber.Ctx) error {
	var req RequestPasswordResetRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	result, err := h.service.RequestPasswordReset(
		c.Context(),
		req.Email,
	)
	if err != nil {
		return err
	}

	response := RequestPasswordResetResponse{
		VerificationID: result.VerificationID,
	}

	return httpx.Created(
		c,
		"password reset requested",
		&response,
	)
}

// CreatePIN godoc
// @Summary Create verification PIN
// @Description Generate and send a verification PIN for a pending password reset.
// @Description Possible error codes:
// @Description - VERIFICATION_NOT_PENDING
// @Description - PASSWORD_RESET_NOT_PENDING
// @Description - PASSWORD_RESET_EXPIRED
// @Description - RESOURCE_NOT_FOUND
// @Description - INTERNAL_SERVER_ERROR
// @Tags Password Reset
// @Produce json
// @Param verification_id path string true "Verification ID"
// @Success 201 {object} CreatePINAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid verification ID"
// @Failure 404 {object} swagger.APIErrorResponse "Verification not found"
// @Failure 409 {object} swagger.APIErrorResponse "Verification or password reset conflict"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/password-reset/verification/{verification_id}/pin [post]
func (h *Handler) CreatePIN(c fiber.Ctx) error {
	verificationID, err := uuid.Parse(c.Params("verification_id"))
	if err != nil {
		return apperror.BadRequestWith(
			"",
			"invalid verification id",
			err,
		)
	}

	result, err := h.service.CreatePIN(
		c.Context(),
		verificationID,
	)
	if err != nil {
		return err
	}

	response := CreatePINResponse{
		VerificationID: result.VerificationID.String(),
	}

	return httpx.Created(
		c,
		"verification code created",
		&response,
	)
}

// GetVerification godoc
// @Summary Get password reset verification
// @Description Get the current status of a password reset verification request.
// @Description Possible error codes:
// @Description - VERIFICATION_NOT_PENDING
// @Description - PASSWORD_RESET_NOT_PENDING
// @Description - PASSWORD_RESET_EXPIRED
// @Description - RESOURCE_NOT_FOUND
// @Description - INTERNAL_SERVER_ERROR
// @Tags Password Reset
// @Produce json
// @Param verification_id path string true "Verification ID"
// @Success 200 {object} GetVerificationAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid verification ID"
// @Failure 404 {object} swagger.APIErrorResponse "Verification not found"
// @Failure 409 {object} swagger.APIErrorResponse "Verification or password reset conflict"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/password-reset/verification/{verification_id} [get]
func (h *Handler) GetVerification(c fiber.Ctx) error {
	verificationID, err := uuid.Parse(c.Params("verification_id"))
	if err != nil {
		return apperror.BadRequestWith(
			"",
			"invalid verification id",
			err,
		)
	}

	result, err := h.service.GetVerification(
		c.Context(),
		verificationID,
	)
	if err != nil {
		return err
	}

	response := mapGetVerificationResponse(result)

	return httpx.OK(
		c,
		"verification retrieved",
		&response,
	)
}

// ResendVerification godoc
// @Summary Resend password reset verification PIN
// @Description Generate and send a new verification PIN for a pending password reset.
// @Description Possible error codes:
// @Description - VERIFICATION_NOT_PENDING
// @Description - PASSWORD_RESET_NOT_PENDING
// @Description - PASSWORD_RESET_EXPIRED
// @Description - VERIFICATION_RESEND_COOLDOWN
// @Description - RESOURCE_NOT_FOUND
// @Description - INTERNAL_SERVER_ERROR
// @Tags Password Reset
// @Produce json
// @Param verification_id path string true "Verification ID"
// @Success 200 {object} ResendVerificationAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid verification ID"
// @Failure 404 {object} swagger.APIErrorResponse "Verification not found"
// @Failure 409 {object} swagger.APIErrorResponse "Verification or password reset conflict"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/password-reset/verification/{verification_id}/resend [post]
func (h *Handler) ResendVerification(c fiber.Ctx) error {
	verificationID, err := uuid.Parse(c.Params("verification_id"))
	if err != nil {
		return apperror.BadRequestWith(
			"",
			"invalid verification id",
			err,
		)
	}

	result, err := h.service.ResendVerification(
		c.Context(),
		verificationID,
	)
	if err != nil {
		return err
	}

	response := ResendVerificationResponse{
		VerificationID: result.VerificationID.String(),
	}

	return httpx.OK(
		c,
		"verification code resent",
		&response,
	)
}

// VerifyPasswordReset godoc
// @Summary Verify password reset
// @Description Verify a password reset using the verification PIN.
// @Description Possible error codes:
// @Description - VERIFICATION_NOT_PENDING
// @Description - PASSWORD_RESET_NOT_PENDING
// @Description - PASSWORD_RESET_EXPIRED
// @Description - INVALID_VERIFICATION_CODE
// @Description - VERIFICATION_CODE_ATTEMPTS_EXCEEDED
// @Description - RESOURCE_NOT_FOUND
// @Description - INTERNAL_SERVER_ERROR
// @Tags Password Reset
// @Accept json
// @Produce json
// @Param verification_id path string true "Verification ID"
// @Param request body VerifyPasswordResetRequest true "Password reset verification request"
// @Success 200 {object} VerifyPasswordResetAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid request"
// @Failure 404 {object} swagger.APIErrorResponse "Verification not found"
// @Failure 409 {object} swagger.APIErrorResponse "Verification conflict"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/password-reset/verification/{verification_id}/verify [post]
func (h *Handler) VerifyPasswordReset(c fiber.Ctx) error {
	verificationID, err := uuid.Parse(c.Params("verification_id"))
	if err != nil {
		return apperror.BadRequestWith(
			"",
			"invalid verification id",
			err,
		)
	}

	var req VerifyPasswordResetRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	result, err := h.service.VerifyPasswordReset(
		c.Context(),
		verificationID,
		req.PIN,
	)
	if err != nil {
		return err
	}

	response := VerifyPasswordResetResponse{
		PasswordResetContinuationToken: result.PasswordResetContinuationToken,
	}

	return httpx.OK(
		c,
		"password reset verified",
		&response,
	)
}

// GetPasswordResetContinuation godoc
// @Summary Get password reset continuation
// @Description Validate and retrieve the password reset continuation associated with the continuation token.
// @Description Possible error codes:
// @Description - PASSWORD_RESET_CONTINUATION_REQUIRED
// @Description - PASSWORD_RESET_CONTINUATION_CONSUMED
// @Description - PASSWORD_RESET_CONTINUATION_EXPIRED
// @Description - PASSWORD_RESET_NOT_PENDING
// @Description - PASSWORD_RESET_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags Password Reset
// @Produce json
// @Param X-Password-Reset-Continuation header string true "Password reset continuation token"
// @Success 200 {object} GetPasswordResetContinuationAPIResponse
// @Failure 401 {object} swagger.APIErrorResponse "Password reset continuation token is required"
// @Failure 409 {object} swagger.APIErrorResponse "Password reset continuation is invalid or expired"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/password-reset/continuation [get]
func (h *Handler) GetPasswordResetContinuation(c fiber.Ctx) error {
	token, err := extractPasswordResetContinuationToken(c)
	if err != nil {
		return err
	}

	result, err := h.service.GetPasswordResetContinuation(
		c.Context(),
		token,
	)
	if err != nil {
		return err
	}

	response := GetPasswordResetContinuationResponse{
		Email:                 result.Email,
		HasPasswordCredential: result.HasPasswordCredential,
	}

	return httpx.OK(
		c,
		"password reset continuation is valid",
		&response,
	)
}


// FinalizePasswordReset godoc
// @Summary Finalize password reset
// @Description Complete a password reset using a valid password reset continuation token.
// @Description Possible error codes:
// @Description - PASSWORD_RESET_CONTINUATION_REQUIRED
// @Description - PASSWORD_RESET_CONTINUATION_CONSUMED
// @Description - PASSWORD_RESET_CONTINUATION_EXPIRED
// @Description - PASSWORD_RESET_NOT_PENDING
// @Description - PASSWORD_RESET_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags Password Reset
// @Accept json
// @Produce json
// @Param X-Password-Reset-Continuation header string true "Password reset continuation token"
// @Param request body FinalizePasswordResetRequest true "Password reset request"
// @Success 200 {object} FinalizePasswordResetAPIResponse
// @Failure 400 {object} swagger.ValidationErrorResponse "Validation error"
// @Failure 401 {object} swagger.APIErrorResponse "Password reset continuation token is required"
// @Failure 409 {object} swagger.APIErrorResponse "Password reset continuation is invalid or expired"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/password-reset/finalize [post]
func (h *Handler) FinalizePasswordReset(c fiber.Ctx) error {
	var req FinalizePasswordResetRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	continuationToken, err := extractPasswordResetContinuationToken(c)
	if err != nil {
		return err
	}

	err = h.service.FinalizePasswordReset(
		c.Context(),
		FinalizePasswordResetInput{
			ContinuationToken: continuationToken,
			Password:          req.Password,
		},
	)
	if err != nil {
		return err
	}

	return httpx.OKMessage(
		c,
		"password reset completed successfully",
	)
}