package client_release

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/httpx"
)

type Handler struct {
	service Service
}

func NewHandler(service Service) *Handler {
	return &Handler{
		service: service,
	}
}

// Check godoc
// @Summary Check for a client release
// @Description Reports whether the calling client should update.
// @Description status is none, optional or required and is computed by the server from the client's build, the platform's minimum supported build and the latest release. Clients must not compare version strings.
// @Description When the platform has no release yet, status is none and latest and min_supported are null.
// @Description Latest.build may be lower than current_build when the client is ahead of the published release; status is none in that case and means no action.
// @Description Possible error codes:
// @Description - INVALID_PLATFORM
// @Description - INVALID_CLIENT_BUILD
// @Description - INTERNAL_SERVER_ERROR
// @Tags Client Release
// @Produce json
// @Param platform query string true "Client platform" Enums(android, windows) example(android)
// @Param build query integer true "Client build number (Android versionCode)" minimum(1) example(4)
// @Success 200 {object} CheckAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid platform or build"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /client-releases/latest [get]
func (h *Handler) Check(c fiber.Ctx) error {
	// Set before binding so an error response is uncached too. The decision can
	// change with a single policy update, so caching it anywhere on the path could
	// delay a forced update or keep offering a rolled-back release.
	c.Set(fiber.HeaderCacheControl, "no-store")

	var req CheckRequest

	if err := httpx.BindQuery(c, &req); err != nil {
		return err
	}

	result, err := h.service.Check(
		c.Context(),
		req.Platform,
		req.Build,
	)
	if err != nil {
		return err
	}

	response := mapCheckResponse(result)

	return httpx.OK(
		c,
		"client release check completed successfully",
		&response,
	)
}
