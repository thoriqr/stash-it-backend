package login

import (
	"context"
	"errors"

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
	audiences []string

	// validate is idtoken.Validate in production. It is a field so tests can
	// exercise audience selection without contacting Google's key endpoint; it
	// is never replaced by production wiring.
	validate func(
		ctx context.Context,
		idToken string,
		audience string,
	) (*idtoken.Payload, error)
}

// NewGoogleTokenVerifier accepts ID tokens issued to the configured Google
// clients.
//
// webClientID is required by configuration. desktopClientID is optional and is
// only added when set, so a deployment without a desktop client keeps the
// existing single-audience behaviour. Both values remain explicit audiences: a
// token is accepted only after idtoken.Validate succeeds for one of them.
func NewGoogleTokenVerifier(
	webClientID string,
	desktopClientID string,
) GoogleTokenVerifier {
	audiences := []string{webClientID}
	if desktopClientID != "" && desktopClientID != webClientID {
		audiences = append(audiences, desktopClientID)
	}

	return &googleTokenVerifier{
		audiences: audiences,
		validate:  idtoken.Validate,
	}
}

func (v *googleTokenVerifier) Verify(
	ctx context.Context,
	rawIDToken string,
) (GoogleIdentity, error) {
	payload, err := v.validateForAnyAudience(
		ctx,
		rawIDToken,
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

// validateForAnyAudience tries each configured audience in turn.
//
// idtoken.Validate takes one audience and performs the full signature, issuer,
// expiry and audience checks for it. Repeating it per configured audience keeps
// all of those checks intact for every accepted token; no caller-supplied
// audience is ever trusted, and a token whose audience matches none of the
// configured values is refused.
func (v *googleTokenVerifier) validateForAnyAudience(
	ctx context.Context,
	rawIDToken string,
) (*idtoken.Payload, error) {
	var lastErr error

	for _, audience := range v.audiences {
		payload, err := v.validate(
			ctx,
			rawIDToken,
			audience,
		)
		if err == nil {
			return payload, nil
		}

		lastErr = err
	}

	if lastErr == nil {
		lastErr = errors.New("no Google audiences configured")
	}

	return nil, lastErr
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
