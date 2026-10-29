package enrichment

import (
	"errors"
	"net/url"
	"strings"
)

// SanitizedURL renders a user-supplied URL for a log field.
//
// The query string and the fragment are dropped rather than logged. They are
// routinely tracking parameters, session identifiers or signed tokens, and none
// of them describes the page that was fetched. Userinfo is dropped for the same
// reason: it is a credential, and a log is not where one belongs.
//
// Scheme, host and path are kept, because they are what identifies the page in a
// failure report. A value that cannot be parsed is reported as a placeholder
// instead of being echoed, so a log cannot become the place a malformed payload
// leaks from.
func SanitizedURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "<invalid>"
	}

	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""

	return parsed.String()
}

// SafeErrorMessage renders err for a log field with any URL it carries removed.
//
// A failed request arrives as a *url.Error, whose text is `Get "URL": cause`.
// The standard library masks the password in that URL but keeps the user name,
// the query string and the fragment, so the message itself is not safe to log
// even when the URL is also logged separately in sanitized form.
//
// The URL-bearing rendering is replaced with its cause, which is the diagnostic
// part: a refused address, a connection error, a stream reset, or a timeout.
//
// An error that carries no *url.Error is returned unchanged. Every other error
// this package produces is built from a sentinel, a status code or a content
// type, none of which contains a URL.
func SafeErrorMessage(err error) string {
	if err == nil {
		return ""
	}

	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return err.Error()
	}

	// Every wrapper in this package embeds the cause's rendering verbatim, so
	// replacing the whole url.Error rendering with its cause removes the URL
	// without disturbing the surrounding context.
	return strings.ReplaceAll(
		err.Error(),
		urlErr.Error(),
		urlErr.Err.Error(),
	)
}
