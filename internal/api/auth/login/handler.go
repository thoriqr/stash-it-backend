package login

import (
	"github.com/google/uuid"

	"github.com/gofiber/fiber/v3"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
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

func (h *Handler) LoginManual(c fiber.Ctx) error {
	var req LoginManualRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	metadata, err := session.ExtractMetadata(c)
	if err != nil {
		return err
	}

	result, err := h.service.LoginManual(
		c.Context(),
		req.Email,
		req.Password,
		metadata,
	)
	if err != nil {
		return err
	}

	response := mapLoginManualResponse(result)

	return httpx.OK(
		c,
		"login successful",
		&response,
	)
}

func (h *Handler) LoginGoogle(c fiber.Ctx) error {
	var req LoginGoogleRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	metadata, err := session.ExtractMetadata(c)
	if err != nil {
		return err
	}

	result, err := h.service.LoginGoogle(
		c.Context(),
		req.IDToken,
		metadata,
	)
	if err != nil {
		return err
	}

	response := mapLoginGoogleResponse(result)

	return httpx.OK(
		c,
		"login successful",
		&response,
	)
}

func (h *Handler) GetAccountLinkConfirmation(c fiber.Ctx) error {
	confirmationID, err := uuid.Parse(c.Params("confirmation_id"))
	if err != nil {
		return err
	}

	result, err := h.service.GetAccountLinkConfirmation(
		c.Context(),
		confirmationID,
	)
	if err != nil {
		return err
	}

	response := GetAccountLinkConfirmationResponse{
		ID:                  result.ID.String(),
		Provider:            result.Provider,
		EmailSnapshot:       result.EmailSnapshot,
		DisplayNameSnapshot: result.DisplayNameSnapshot,
		UserEmail:           result.UserEmail,
		UserDisplayName:     result.UserDisplayName,
	}

	return httpx.OK(
		c,
		"account link confirmation retrieved successfully",
		&response,
	)
}

func (h *Handler) ConfirmAccountLink(c fiber.Ctx) error {
	confirmationID, err := uuid.Parse(c.Params("confirmation_id"))
	if err != nil {
		return err
	}

	metadata, err := session.ExtractMetadata(c)
	if err != nil {
		return err
	}

	result, err := h.service.ConfirmAccountLink(
		c.Context(),
		confirmationID,
		metadata,
	)
	if err != nil {
		return err
	}

	response := mapLoginGoogleResponse(result)

	return httpx.OK(
		c,
		"account link confirmed successfully",
		&response,
	)
}