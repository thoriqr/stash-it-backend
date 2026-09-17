package middleware

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

const (
	CodeInvalidAuthorizationHeader = "INVALID_AUTHORIZATION_HEADER"
	CodeInvalidAccessToken         = "INVALID_ACCESS_TOKEN"
	CodeAccessTokenExpired         = "ACCESS_TOKEN_EXPIRED"
)

const AuthClaimsKey = "auth_claims"
const authorizationHeader = "Authorization"

func Auth(verifier *security.AccessTokenVerifier) fiber.Handler {
	return func(c fiber.Ctx) error {
		header := c.Get(authorizationHeader)

		const prefix = "Bearer "

		if !strings.HasPrefix(header, prefix) {
			return apperror.UnauthorizedWith(
				CodeInvalidAuthorizationHeader,
				"missing or invalid authorization header",
				nil,
			)
		}

		tokenString := strings.TrimSpace(strings.TrimPrefix(header, prefix))
		if tokenString == "" {
			return apperror.UnauthorizedWith(
				CodeInvalidAuthorizationHeader,
				"missing or invalid authorization header",
				nil,
			)
		}

	claims, err := verifier.Verify(tokenString)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return apperror.UnauthorizedWith(
				CodeAccessTokenExpired,
				"access token expired",
				err,
			)
		}

		return apperror.UnauthorizedWith(
			CodeInvalidAccessToken,
			"invalid access token",
			err,
		)
	}

		c.Locals(AuthClaimsKey, claims)

		return c.Next()
	}
}