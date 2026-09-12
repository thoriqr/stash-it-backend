package registration

import (
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler  {
	return &Handler {
		service: service,
	}
}

func (h *Handler) RegisterManual(c fiber.Ctx) error {
	var req RegisterRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	result, err := h.service.RegisterManual(
		c.Context(),
		req.Email,
	)
	if err != nil {
		return err
	}

	message := "verification code sent"

	if result.AlreadyPending {
    message = "registration already in progress"
}

	response := RegisterResponse{
		VerificationID: result.VerificationID,
	}

	return httpx.Created(
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


func (h *Handler) VerifyRegistration(c fiber.Ctx) error {
	verificationID, err := uuid.Parse(c.Params("verification_id"))
	if err != nil {
		return apperror.BadRequestWith(
			"",
			"invalid verification id",
			err,
		)
	}

	var request VerifyRegistrationRequest

	if err := httpx.BindBody(c, &request); err != nil {
		return err
	}

	result, err := h.service.VerifyRegistration(
		c.Context(),
		verificationID,
		request.PIN,
	)
	if err != nil {
		return err
	}

	response := VerifyRegistrationResponse{
		RegistrationContinuationToken: result.RegistrationContinuationToken,
	}

	return httpx.OK(
		c,
		"registration verified",
		&response,
	)
}

func (h *Handler) GetRegistrationContinuation(c fiber.Ctx) error {
	token, err := extractRegistrationContinuationToken(c)
	if err != nil {
		return err
	}

	result, err := h.service.GetRegistrationContinuation(
		c.Context(),
		token,
	)
	if err != nil {
		return err
	}

	response := GetRegistrationContinuationResponse{
		Email:            result.Email,
		RegistrationType: string(result.RegistrationType),
		RequiresPassword: result.RequiresPassword,
	}

	return httpx.OK(
		c,
		"registration continuation is valid",
		&response,
	)
}

func (h *Handler) FinalizeManualRegistration(c fiber.Ctx) error {
    var req FinalizeManualRegistrationRequest

    if err := httpx.BindBody(c, &req); err != nil {
        return err
    }

    continuationToken, err := extractRegistrationContinuationToken(c)
    if err != nil {
        return err
    }

    result, err := h.service.FinalizeManualRegistration(
        c.Context(),
        FinalizeManualRegistrationInput{
            ContinuationToken: continuationToken,
            DisplayName:       req.DisplayName,
            Password:          req.Password,
        },
    )
    if err != nil {
        return err
    }

    response := FinalizeManualRegistrationResponse{
        Email:       result.Email,
        DisplayName: result.DisplayName,
    }

    return httpx.OK(
        c,
        "registration completed successfully",
        &response,
    )
}