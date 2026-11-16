package enrichment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// oEmbed enrichment.
//
// oEmbed is a small, standard JSON format a provider exposes for one of its
// pages. It is used here as the first source of metadata because it is far
// cheaper than a page and because it is the only reliable way to read hosts
// whose HTML is a bot challenge or a client-side shell.
//
// The response format is one specification with optional fields, not one format
// per provider. Only the fields this package can store are decoded, and every
// one of them is optional:
//
//   - title is the human readable title.
//   - provider_name is the platform's own name.
//   - thumbnail_url is the preview image for video and rich content.
//   - url is the image for a photo response.
//   - description is not part of the specification. It is read when a provider
//     sends it as an extension, and is otherwise absent.
//
// Width, height and html are deliberately not decoded. They are not stored, and
// their JSON types vary between providers, so decoding them would only add a way
// for an otherwise usable response to fail.

// OEmbedEndpoint is a provider's oEmbed endpoint, already selected for a URL.
//
// ProviderName is the provider's own name from the registry. It is a fallback for
// the response's provider_name, never a replacement for it: the response is the
// provider speaking about itself, which is the same authority every other
// platform signal in this package uses.
type OEmbedEndpoint struct {
	ProviderName string
	EndpointURL  string

	// SkipHTMLFallback marks the endpoint as the only usable way to read the
	// host. YouTube is the current case: its watch pages are a client-side shell
	// from a datacenter, so a failed oEmbed request is a final answer rather than
	// a reason to fetch the page. A registry provider leaves this false and still
	// falls back to HTML.
	SkipHTMLFallback bool
}

// ProviderResolver maps a URL to the oEmbed endpoint that can answer for it.
//
// It is declared here, in the consuming package, for the same reason every other
// narrow dependency in this codebase is: the consumer states the interface it
// uses. The production implementation is internal/oembedregistry, which reads
// the community provider list and caches it. Enrichment itself never fetches the
// registry and never knows where it came from.
//
// A false return means no provider is known for the URL. It is not an error and
// must not stop enrichment: the HTML path still runs.
type ProviderResolver interface {
	Resolve(
		ctx context.Context,
		parsed *url.URL,
	) (OEmbedEndpoint, bool)
}

// oembedMaxResponseBytes bounds an oEmbed body.
//
// Real responses are a few kilobytes. The generous ceiling leaves room for a
// provider that embeds a large html fragment without letting a misdirected
// endpoint stream something large.
const oembedMaxResponseBytes int64 = 256 << 10

// oEmbedResponse is the part of an oEmbed document this package reads.
//
// The fields are plain strings rather than pointers because an absent field and
// an empty field are the same thing here: optionalText turns both into nil. The
// decoder ignores unknown fields, which is what makes one struct enough for
// every provider.
type oEmbedResponse struct {
	Type         string `json:"type"`
	Version      string `json:"version"`
	Title        string `json:"title"`
	AuthorName   string `json:"author_name"`
	ProviderName string `json:"provider_name"`
	ProviderURL  string `json:"provider_url"`
	ThumbnailURL string `json:"thumbnail_url"`
	URL          string `json:"url"`
	Description  string `json:"description"`
}

// enrichWithOEmbed asks one endpoint about one URL and maps the answer.
//
// client must be the guarded client chosen for the submitted host, so the
// request carries the same dialer, redirect limit and size limits as every other
// outbound fetch. The endpoint itself comes from the registry or from a page's
// discovery link, and either way it is fetched through that same client.
//
// overrideTarget controls the url parameter. A registry endpoint is asked about
// the submitted URL, so it is always overridden. A discovery link already names
// the page it belongs to, so its url is kept and only supplied when absent.
func (e *enricher) enrichWithOEmbed(
	ctx context.Context,
	client *http.Client,
	parsed *url.URL,
	rawURL string,
	endpoint OEmbedEndpoint,
	overrideTarget bool,
) (Metadata, error) {
	requestURL, err := buildOEmbedURL(
		endpoint.EndpointURL,
		oembedTargetURL(parsed, rawURL),
		overrideTarget,
	)
	if err != nil {
		return Metadata{}, newFailure(
			FailureContent,
			fmt.Errorf("%w: %w", ErrInvalidOEmbedResponse, err),
		)
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		requestURL,
		nil,
	)
	if err != nil {
		return Metadata{}, newFailure(
			FailureContent,
			fmt.Errorf("%w: %w", ErrInvalidURL, err),
		)
	}

	request.Header.Set("Accept", "application/json")

	// An oEmbed response carries provider_name, which a provider can localize.
	// Asking in one language keeps that name from depending on the worker's
	// address, and the canonical platform rule then reduces it either way.
	request.Header.Set("Accept-Language", enrichAcceptLanguage)

	response, err := client.Do(request)
	if err != nil {
		return Metadata{}, newFailure(
			requestFailureKind(err),
			fmt.Errorf("%w: %w", ErrFetchFailed, err),
		)
	}

	defer response.Body.Close()

	if response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices {
		failure := newFailure(
			statusFailureKind(response.StatusCode),
			fmt.Errorf(
				"%w: status %d",
				ErrUnexpectedStatus,
				response.StatusCode,
			),
		)
		failure.StatusCode = response.StatusCode

		return Metadata{}, failure
	}

	body, err := io.ReadAll(io.LimitReader(
		response.Body,
		oembedMaxResponseBytes,
	))
	if err != nil {
		return Metadata{}, newFailure(
			FailureFetch,
			fmt.Errorf("%w: %w", ErrFetchFailed, err),
		)
	}

	var payload oEmbedResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return Metadata{}, newFailure(
			FailureContent,
			fmt.Errorf(
				"%w: %w",
				ErrInvalidOEmbedResponse,
				err,
			),
		)
	}

	// A photo response carries the image in url rather than thumbnail_url. The
	// specification uses the two names for different response types, so both are
	// read and the photo form is the fallback.
	image := firstNonEmpty(
		payload.ThumbnailURL,
		photoOEmbedImage(payload),
	)

	var imageBase *url.URL
	if payload.ProviderURL != "" {
		imageBase, _ = url.Parse(payload.ProviderURL)
	}

	return Metadata{
		Title: optionalText(payload.Title),
		Platform: optionalPlatformName(
			firstNonEmpty(payload.ProviderName, endpoint.ProviderName),
		),
		Description: optionalText(payload.Description),
		ImageURL:    optionalURL(image, imageBase),
		Author:      optionalText(payload.AuthorName),
	}, nil
}

// photoOEmbedImage returns the image of a photo response.
func photoOEmbedImage(payload oEmbedResponse) string {
	if strings.EqualFold(payload.Type, "photo") {
		return payload.URL
	}

	return ""
}

// oembedTargetURL returns the URL an endpoint should be asked about.
//
// Most providers accept the submitted URL unchanged. YouTube is the exception
// this package already knows how to normalize: a short link, a /shorts/ URL and a
// watch URL with tracking parameters all address the same video, and the oEmbed
// endpoint is asked about the canonical watch URL so the answer does not depend on
// which form was shared.
func oembedTargetURL(
	parsed *url.URL,
	rawURL string,
) string {
	if videoID, ok := youtubeVideoID(parsed); ok {
		return youtubeCanonicalWatchURL + videoID
	}

	return rawURL
}

// buildOEmbedURL completes an oEmbed endpoint for one target URL.
//
// {format} is part of the provider registry's endpoint notation rather than part
// of a URL, so it is replaced before parsing. The query is then completed rather
// than rebuilt, because a discovery link already carries the parameters it needs.
func buildOEmbedURL(
	endpoint string,
	target string,
	overrideTarget bool,
) (string, error) {
	normalized := strings.ReplaceAll(endpoint, "{format}", "json")

	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf(
			"oembed endpoint scheme %q is not supported",
			parsed.Scheme,
		)
	}

	if parsed.Host == "" {
		return "", fmt.Errorf("oembed endpoint has no host")
	}

	query := parsed.Query()

	if overrideTarget || query.Get("url") == "" {
		query.Set("url", target)
	}

	if query.Get("format") == "" {
		query.Set("format", "json")
	}

	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}
