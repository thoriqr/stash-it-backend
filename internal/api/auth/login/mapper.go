package login

import (
	"strings"
	"unicode/utf8"
)

// maskEmail redacts an email address for display beside an account the reader has
// not authenticated as.
//
// It keeps the first character of the local part and the whole domain, so a
// reader can still recognize an address as theirs, and replaces the rest with
// "***": alice@example.com becomes a***@example.com.
//
// The domain is kept whole deliberately. It is the part that disambiguates a
// personal address from a work one at the same company, and it is not what makes
// an address contactable — the local part is. Redacting the domain instead would
// remove the recognition without removing the means of contact.
//
// An address it cannot take apart yields the empty string rather than the
// original. That is the whole point of the function, so there is no fallback that
// could return what it was asked to hide: an address this cannot mask is an
// address the caller must not be shown at all. A blank field renders as no hint,
// which is a worse experience and a correct one.
//
// The separator is the LAST "@", because a local part may legally contain one
// inside quotes ("a@b"@example.com). Splitting at the first would put part of the
// local part into what is presented as the domain, which is the one thing this
// function must never do.
func maskEmail(email string) string {
	separator := strings.LastIndex(email, "@")

	// Nothing before it, nothing after it, or no separator at all: this is not an
	// address this can redact, so it reports nothing.
	if separator <= 0 || separator == len(email)-1 {
		return ""
	}

	// Decoding rather than slicing keeps the result valid UTF-8 for a local part
	// that does not start with an ASCII character. Slicing the first byte would
	// produce a replacement character at best and invalid output at worst.
	first, _ := utf8.DecodeRuneInString(email[:separator])

	return string(first) + "***@" + email[separator+1:]
}

func mapLoginManualResponse(result LoginResult) LoginManualResponse {
	return LoginManualResponse{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		User: LoginUser{
			ID:          result.User.ID.String(),
			Email:       result.User.Email,
			DisplayName: result.User.DisplayName,
		},
	}
}

func mapLoginGoogleResponse(result LoginGoogleResult) LoginGoogleResponse {
	response := LoginGoogleResponse{
		Outcome: result.Outcome,
	}

	switch result.Outcome {
	case LoginOutcomeAuthenticated:
		response.AccessToken = result.AccessToken
		response.RefreshToken = result.RefreshToken

		if result.User != nil {
			response.User = &LoginUser{
				ID:          result.User.ID.String(),
				Email:       result.User.Email,
				DisplayName: result.User.DisplayName,
			}
		}

	case LoginOutcomeAccountLinkRequired:
		response.Provider = "google"
		response.ConfirmationID = result.ConfirmationID.String()

	case LoginOutcomeRegistrationRequired:
		response.VerificationID = result.VerificationID.String()
	}

	return response
}