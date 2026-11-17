package enrichment

import (
	"net/url"
	"strings"
)

// Instagram titles.
//
// An Instagram reel or post publishes its Open Graph title as
// `Author on Instagram: "<caption>"` and its description as
// `34K likes, ... - author on <date>: "<same caption>"`. The two are not equal, so
// the duplicate-description rule does not apply, but the title is a whole caption
// with an author prefix rather than a title. Storing it makes a saved item's title
// and description read as the same paragraph.
//
// The period before the caption is a stable part of the format, so the title is
// shortened to `Author on Instagram` and the caption stays where it belongs, in
// the description. A page that does not match the format is left alone.

// instagramHosts are the hostnames whose titles use the author-prefix format.
var instagramHosts = map[string]struct{}{
	"instagram.com":     {},
	"www.instagram.com": {},
}

func isInstagramHost(hostname string) bool {
	_, ok := instagramHosts[normalizeHostname(hostname)]

	return ok
}

// instagramAuthorMarker ends the author prefix of an Instagram title.
const instagramAuthorMarker = " on instagram:"

// applyInstagramTitle replaces a caption-bearing title with its author prefix.
func applyInstagramTitle(
	pageURL *url.URL,
	metadata Metadata,
) Metadata {
	metadata.Title = ShortenInstagramTitle(pageURL, metadata.Title)

	return metadata
}

// ShortenInstagramTitle returns the author label for a title in the
// "Author on Instagram: ..." format, or the title unchanged.
//
// It is exported because the client-assisted path accepts a title the server did
// not extract, and the same rule must apply there. Without it a client that sends
// the raw Open Graph title stores the whole caption as the title, which then reads
// as the same paragraph as the description.
//
// The marker is searched case-insensitively because the page capitalizes
// "Instagram" but the surrounding text is not reliable. A title that does not
// contain the marker, or whose author part is empty, is left alone.
func ShortenInstagramTitle(
	pageURL *url.URL,
	title *string,
) *string {
	if title == nil || pageURL == nil ||
		!isInstagramHost(pageURL.Hostname()) {
		return title
	}

	value := *title
	index := strings.Index(strings.ToLower(value), instagramAuthorMarker)
	if index <= 0 {
		return title
	}

	author := strings.TrimSpace(value[:index])
	if author == "" {
		return title
	}

	shortened := author + " on Instagram"

	return &shortened
}
