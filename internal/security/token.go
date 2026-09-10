package security

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

const tokenLength = 32

func GenerateToken() (string, error) {
	token := make([]byte, tokenLength)

	if _, err := rand.Read(token); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(token), nil
}

func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))

	return base64.RawURLEncoding.EncodeToString(hash[:])
}