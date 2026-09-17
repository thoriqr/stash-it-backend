package login

import (
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