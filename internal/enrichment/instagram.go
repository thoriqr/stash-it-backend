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
//
// The prefix is kept exactly as published, including its own spacing, and the
// canonical suffix is appended so the value reads as a label rather than a
// sentence fragment. The marker is searched case-insensitively because the page
// capitalizes "Instagram" but the surrounding text is not reliable.
func applyInstagramTitle(
	pageURL *url.URL,
	metadata Metadata,
) Metadata {
	if metadata.Title == nil || pageURL == nil ||
		!isInstagramHost(pageURL.Hostname()) {
		return metadata
	}

	title := *metadata.Title
	index := strings.Index(strings.ToLower(title), instagramAuthorMarker)
	if index <= 0 {
		return metadata
	}

	author := strings.TrimSpace(title[:index])
	if author == "" {
		return metadata
	}

	shortened := author + " on Instagram"
	metadata.Title = optionalText(shortened)

	return metadata
}
