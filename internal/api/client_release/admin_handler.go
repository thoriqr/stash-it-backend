package client_release

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/httpx"
)

type AdminHandler struct {
	service AdminService
}

func NewAdminHandler(service AdminService) *AdminHandler {
	return &AdminHandler{
		service: service,
	}
}

// PublishRelease godoc
// @Summary Publish a client release
// @Description Record an immutable client release and make it the platform's latest.
// @Description The release must have a build above the current latest. force also makes it the minimum supported build; it is false by default.
// @Description Possible error codes:
// @Description - INVALID_RELEASE_SECRET
// @Description - INVALID_PLATFORM
// @Description - INVALID_RELEASE_METADATA
// @Description - CLIENT_RELEASE_ALREADY_EXISTS
// @Description - INVALID_RELEASE_POLICY
// @Description - INTERNAL_SERVER_ERROR
// @Tags Internal
// @Accept json
// @Produce json
// @Param X-Release-Secret header string true "Release maintenance secret"
// @Param request body PublishReleaseRequest true "Release to publish"
// @Success 201 {object} PublishReleaseAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid platform or release metadata"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or missing release secret"
// @Failure 409 {object} swagger.APIErrorResponse "Release already exists or would violate the release policy"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /internal/maintenance/client-releases [post]
func (h *AdminHandler) PublishRelease(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")

	var req PublishReleaseRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	result, err := h.service.PublishRelease(c.Context(), req)
	if err != nil {
		return err
	}

	response := mapPublishReleaseResponse(result)

	return httpx.Created(
		c,
		"client release published successfully",
		&response,
	)
}

// ForceRelease godoc
// @Summary Force a client release
// @Description Set the platform's minimum supported build to an existing release.
// @Description The target build must not be above the current latest release.
// @Description Possible error codes:
// @Description - INVALID_RELEASE_SECRET
// @Description - INVALID_PLATFORM
// @Description - INVALID_RELEASE_METADATA
// @Description - RELEASE_NOT_FOUND
// @Description - INVALID_RELEASE_POLICY
// @Description - INTERNAL_SERVER_ERROR
// @Tags Internal
// @Accept json
// @Produce json
// @Param X-Release-Secret header string true "Release maintenance secret"
// @Param request body ReleaseActionRequest true "Release to force"
// @Success 200 {object} ReleasePolicyAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid platform or build"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or missing release secret"
// @Failure 404 {object} swagger.APIErrorResponse "Release not found"
// @Failure 409 {object} swagger.APIErrorResponse "Release policy would be invalid"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /internal/maintenance/client-releases/force [post]
func (h *AdminHandler) ForceRelease(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")

	var req ReleaseActionRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	policy, err := h.service.ForceRelease(c.Context(), req)
	if err != nil {
		return err
	}

	response := mapReleasePolicy(policy)

	return httpx.OK(
		c,
		"client release policy updated successfully",
		&response,
	)
}

// RollbackRelease godoc
// @Summary Roll back a client release
// @Description Move the platform's latest pointer back to an existing release.
// @Description The target must not be above the current latest. The minimum is lowered only when it would otherwise be above the new latest.
// @Description Possible error codes:
// @Description - INVALID_RELEASE_SECRET
// @Description - INVALID_PLATFORM
// @Description - INVALID_RELEASE_METADATA
// @Description - RELEASE_NOT_FOUND
// @Description - INVALID_RELEASE_POLICY
// @Description - INTERNAL_SERVER_ERROR
// @Tags Internal
// @Accept json
// @Produce json
// @Param X-Release-Secret header string true "Release maintenance secret"
// @Param request body ReleaseActionRequest true "Release to roll back to"
// @Success 200 {object} ReleasePolicyAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid platform or build"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or missing release secret"
// @Failure 404 {object} swagger.APIErrorResponse "Release not found"
// @Failure 409 {object} swagger.APIErrorResponse "Release policy would be invalid"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /internal/maintenance/client-releases/rollback [post]
func (h *AdminHandler) RollbackRelease(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")

	var req ReleaseActionRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	policy, err := h.service.RollbackRelease(c.Context(), req)
	if err != nil {
		return err
	}

	response := mapReleasePolicy(policy)

	return httpx.OK(
		c,
		"client release rolled back successfully",
		&response,
	)
}

// ListReleases godoc
// @Summary List client releases
// @Description List a platform's releases newest first together with its current policy.
// @Description Releases is always an array; latest and min_supported are null when the platform has no channel yet.
// @Description Possible error codes:
// @Description - INVALID_RELEASE_SECRET
// @Description - INVALID_PLATFORM
// @Description - INTERNAL_SERVER_ERROR
// @Tags Internal
// @Produce json
// @Param X-Release-Secret header string true "Release maintenance secret"
// @Param platform query string true "Client platform" Enums(android, windows) example(android)
// @Success 200 {object} ListReleasesAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid platform"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or missing release secret"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /internal/maintenance/client-releases [get]
func (h *AdminHandler) ListReleases(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")

	var req ListReleasesRequest

	if err := httpx.BindQuery(c, &req); err != nil {
		return err
	}

	result, err := h.service.ListReleases(c.Context(), req.Platform)
	if err != nil {
		return err
	}

	response := mapListReleasesResponse(result)

	return httpx.OK(
		c,
		"client releases retrieved successfully",
		&response,
	)
}
