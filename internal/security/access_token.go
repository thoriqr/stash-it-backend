package security

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type AccessTokenGenerator struct {
	secret []byte
}

func NewAccessTokenGenerator(secret []byte) *AccessTokenGenerator {
	return &AccessTokenGenerator{
		secret: secret,
	}
}

type AccessTokenClaims struct {
	jwt.RegisteredClaims
	SessionID uuid.UUID `json:"sid"`
}

func (g *AccessTokenGenerator) Generate(
	userID uuid.UUID,
	sessionID uuid.UUID,
	lifetime time.Duration,
) (string, error) {
	now := time.Now()

	claims := AccessTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(lifetime)),
		},
		SessionID: sessionID,
	}

	token := jwt.NewWithClaims(
		jwt.SigningMethodHS256,
		claims,
	)

	return token.SignedString(g.secret)
}