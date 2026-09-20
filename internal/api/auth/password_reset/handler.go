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

	message := "verification code sent"

	if result.AlreadyPending {
		message = "password reset already in progress"
	}

	response := RequestPasswordResetResponse{
		VerificationID: result.VerificationID,
	}

	return httpx.OK(
		c,
		message,
		&response,
	)
}

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