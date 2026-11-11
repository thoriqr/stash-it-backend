package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// loadEnvFile reads .env.<APP_ENV> when it exists.
//
// A missing file is not an error. A deployment may supply environment variables
// directly through systemd, a container runtime or a hosting platform, and
// godotenv does not override variables that are already set, so those values win
// over anything in the file.
//
// A file that exists but cannot be read or parsed is still an error. That is a
// broken configuration rather than a deployment that chose another source.
func loadEnvFile(appEnv string) error {
	envFile := fmt.Sprintf(".env.%s", appEnv)

	if err := godotenv.Load(envFile); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf(
			"failed to load environment file %q: %w",
			envFile,
			err,
		)
	}

	return nil
}
