package enrichment

import (
	"net/url"
	"strings"
)

// YouTube enrichment through the oEmbed endpoint.
//
// youtube.com is the one host this package does not read as HTML. Its watch pages
// are served differently depending on where the request comes from: from a
// residential address they carry the full Open Graph block, and from a datacenter
// address such as this deployment's VPS they arrive as a shell with an empty
// <title> and no metadata tags at all. That was measured directly, not inferred —
// the same User-Agent produced both responses, so no header can be changed to
// recover the first one.
//
// The oEmbed endpoint answers the same question without sending a page: it
// returns the title and thumbnail as JSON in a few kilobytes, and it is not gated
// by source address. It is therefore the supported way to read this host rather
// than a workaround for one.
const (
	youtubeOEmbedEndpoint = "https://www.youtube.com/oembed"

	// youtubeCanonicalWatchURL builds the URL oEmbed is asked about. A video id is
	// the only part of a submitted URL that determines the answer, so a short
	// link, a /live/ URL and a /watch URL with tracking parameters all resolve to
	// the same request.
	youtubeCanonicalWatchURL = "https://www.youtube.com/watch?v="
)

// youtubeHosts are the hostnames whose video pages oEmbed can answer for.
//
// Membership is by exact, normalised equality. A suffix match would accept
// youtube.com.evil.example, and a substring match would accept notyoutube.com, so
// neither is used. m. and music. are included because the mobile apps and the
// share sheet produce them.
var youtubeHosts = map[string]struct{}{
	"youtube.com":              {},
	"www.youtube.com":          {},
	"m.youtube.com":            {},
	"music.youtube.com":        {},
	"youtu.be":                 {},
	"www.youtu.be":             {},
	"youtube-nocookie.com":     {},
	"www.youtube-nocookie.com": {},
}

// youtubeVideoIDPathSegments are the first path segments that carry a video id as
// the segment after them, as in /shorts/ID or /embed/ID.
var youtubeVideoIDPathSegments = map[string]struct{}{
	"live":   {},
	"shorts": {},
	"embed":  {},
	"v":      {},
	"e":      {},
}

// youtubeVideoID returns the video id a URL addresses, when it addresses one.
//
// A URL that does not identify a single video — a channel, a playlist, or a
// search — reports false and is enriched as HTML instead, because there is
// nothing for oEmbed to answer and no reason to lose the generic path.
func youtubeVideoID(parsed *url.URL) (string, bool) {
	if parsed == nil {
		return "", false
	}

	if _, ok := youtubeHosts[normalizeHostname(parsed.Hostname())]; !ok {
		return "", false
	}

	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")

	switch normalizeHostname(parsed.Hostname()) {
	case "youtu.be", "www.youtu.be":
		return firstValidYouTubeID(segments)

	case "youtube.com", "www.youtube.com", "m.youtube.com",
		"music.youtube.com", "youtube-nocookie.com", "www.youtube-nocookie.com":
		if len(segments) > 0 && segments[0] == "watch" {
			return firstValidYouTubeID(
				[]string{parsed.Query().Get("v")},
			)
		}

		if len(segments) > 1 {
			if _, ok := youtubeVideoIDPathSegments[segments[0]]; ok {
				return firstValidYouTubeID(segments[1:])
			}
		}
	}

	return "", false
}

// firstValidYouTubeID returns the first entry that is a well-formed video id.
//
// The shape is checked rather than trusted because it is put into a query string
// and a thumbnail path: an id is eleven characters from a fixed alphabet, and
// requiring exactly that rejects channel handles, playlist ids and any value a
// caller invented, all of which would otherwise be sent to oEmbed as if they
// identified a video.
func firstValidYouTubeID(candidates []string) (string, bool) {
	for _, candidate := range candidates {
		if isYouTubeVideoID(candidate) {
			return candidate, true
		}
	}

	return "", false
}

// isYouTubeVideoID reports whether id has the shape of a YouTube video id.
func isYouTubeVideoID(id string) bool {
	const videoIDLength = 11

	if len(id) != videoIDLength {
		return false
	}

	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}

	return true
}
