package login

import (
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

const (
	headerPlatform       = "X-Platform"
	headerInstallationID = "X-Installation-ID"
	headerDeviceName     = "X-Device-Name"
)

func extractSessionMetadata(c fiber.Ctx) (LoginSessionMetadata, error) {
	platform := c.Get(headerPlatform)
	installationID := c.Get(headerInstallationID)
	deviceName := c.Get(headerDeviceName)
	userAgent := c.Get("User-Agent")

	var parsedInstallationID pgtype.UUID

	if installationID != "" {
		id, err := uuid.Parse(installationID)
		if err != nil {
			return LoginSessionMetadata{}, apperror.BadRequestWith(
				"",
				"invalid installation id",
				err,
			)
		}

		parsedInstallationID = pgtype.UUID{
			Bytes: id,
			Valid: true,
		}
	}

	var parsedDeviceName pgtype.Text
	if deviceName != "" {
		parsedDeviceName = pgtype.Text{
			String: deviceName,
			Valid:  true,
		}
	}

	var parsedUserAgent pgtype.Text
	if userAgent != "" {
		parsedUserAgent = pgtype.Text{
			String: userAgent,
			Valid:  true,
		}
	}

	return LoginSessionMetadata{
		Platform:       platform,
		InstallationID: parsedInstallationID,
		DeviceName:     parsedDeviceName,
		UserAgent:      parsedUserAgent,
	}, nil
}