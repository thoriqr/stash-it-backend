package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
  DatabaseURL            string
  VerificationCodeSecret string
  AccessTokenSecret      string
  GoogleClientID         string
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

	accessTokenSecret := os.Getenv("ACCESS_TOKEN_SECRET")
	if accessTokenSecret == "" {
		return Config{}, fmt.Errorf("ACCESS_TOKEN_SECRET is required")
	}

	googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
	if googleClientID == "" {
    return Config{}, fmt.Errorf("GOOGLE_CLIENT_ID is required")
	}

	return Config{
		DatabaseURL:            databaseURL,
		VerificationCodeSecret: verificationCodeSecret,
		AccessTokenSecret:      accessTokenSecret,
		GoogleClientID:         googleClientID,
	}, nil
}