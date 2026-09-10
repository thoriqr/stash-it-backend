package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL            string
	VerificationCodeSecret string
}

func Load() (Config, error) {
	_ = godotenv.Load()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	verificationCodeSecret := os.Getenv("VERIFICATION_CODE_SECRET")
	if verificationCodeSecret == "" {
		return Config{}, fmt.Errorf(
			"VERIFICATION_CODE_SECRET is required",
		)
	}

	return Config{
		DatabaseURL:            databaseURL,
		VerificationCodeSecret: verificationCodeSecret,
	}, nil
}