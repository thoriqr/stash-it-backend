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

type AccessTokenVerifier struct {
	secret []byte
}

func NewAccessTokenVerifier(secret []byte) *AccessTokenVerifier {
	return &AccessTokenVerifier{
		secret: secret,
	}
}

func (v *AccessTokenVerifier) Verify(
	tokenString string,
) (AccessTokenClaims, error) {
	var claims AccessTokenClaims

	_, err := jwt.ParseWithClaims(
		tokenString,
		&claims,
		func(token *jwt.Token) (any, error) {
			return v.secret, nil
		},
		jwt.WithValidMethods([]string{
			jwt.SigningMethodHS256.Alg(),
		}),
	)
	if err != nil {
		return AccessTokenClaims{}, err
	}

	if claims.Subject == "" {
		return AccessTokenClaims{}, jwt.ErrTokenRequiredClaimMissing
	}

	if claims.SessionID == uuid.Nil {
		return AccessTokenClaims{}, jwt.ErrTokenRequiredClaimMissing
	}

	return claims, nil
}