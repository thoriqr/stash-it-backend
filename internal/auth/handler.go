package auth

import (
	"github.com/gofiber/fiber/v3"

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

func (h *Handler) CreateUser(c fiber.Ctx) error {
	var req CreateUserRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	user, err := h.service.CreateUser(
		c.Context(),
		req.Email,
	)
	if err != nil {
		return err
	}

	response := UserResponse{
		ID:    user.ID,
		Email: user.Email,
	}

	return httpx.Created(
		c,
		"user created successfully",
		&response,
	)
}