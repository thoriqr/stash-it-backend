package login

import (
	"context"

	"google.golang.org/api/idtoken"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

type GoogleIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
}

type GoogleTokenVerifier interface {
	Verify(
		ctx context.Context,
		idToken string,
	) (GoogleIdentity, error)
}

type googleTokenVerifier struct {
	audience string
}

func NewGoogleTokenVerifier(audience string) GoogleTokenVerifier {
	return &googleTokenVerifier{
		audience: audience,
	}
}

func (v *googleTokenVerifier) Verify(
	ctx context.Context,
	rawIDToken string,
) (GoogleIdentity, error) {
	payload, err := idtoken.Validate(
		ctx,
		rawIDToken,
		v.audience,
	)
	if err != nil {
		return GoogleIdentity{}, invalidGoogleToken(err)
	}

	if payload.Subject == "" {
		return GoogleIdentity{}, invalidGoogleToken(
			nil,
		)
	}

	email, err := getStringClaim(payload, "email")
	if err != nil {
		return GoogleIdentity{}, err
	}

	emailVerified, err := getBoolClaim(payload, "email_verified")
	if err != nil {
		return GoogleIdentity{}, err
	}

	if !emailVerified {
		return GoogleIdentity{}, invalidGoogleToken(nil)
	}

	displayName, err := getOptionalStringClaim(payload, "name")
	if err != nil {
		return GoogleIdentity{}, err
	}

	return GoogleIdentity{
		Subject:       payload.Subject,
		Email:         email,
		EmailVerified: emailVerified,
		DisplayName:   displayName,
	}, nil
}

func getStringClaim(
	payload *idtoken.Payload,
	name string,
) (string, error) {
	value, ok := payload.Claims[name]
	if !ok {
		return "", invalidGoogleToken(nil)
	}

	valueString, ok := value.(string)
	if !ok || valueString == "" {
		return "", invalidGoogleToken(nil)
	}

	return valueString, nil
}

func getOptionalStringClaim(
	payload *idtoken.Payload,
	name string,
) (string, error) {
	value, ok := payload.Claims[name]
	if !ok {
		return "", nil
	}

	valueString, ok := value.(string)
	if !ok {
		return "", invalidGoogleToken(nil)
	}

	return valueString, nil
}

func getBoolClaim(
	payload *idtoken.Payload,
	name string,
) (bool, error) {
	value, ok := payload.Claims[name]
	if !ok {
		return false, invalidGoogleToken(nil)
	}

	valueBool, ok := value.(bool)
	if !ok {
		return false, invalidGoogleToken(nil)
	}

	return valueBool, nil
}

func invalidGoogleToken(err error) error {
	return apperror.UnauthorizedWith(
		CodeInvalidGoogleToken,
		"invalid Google ID token",
		err,
	)
}