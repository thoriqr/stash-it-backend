package enrichment

import (
	"net/url"
	"strings"
)

// Generic document titles.
//
// A page that is a login wall, a client-side shell or a not-found route still has
// a document title, and that title is frequently just the product's name. Storing
// it as a saved item's title is worse than storing nothing: it looks like a real
// title and hides the fact that the page exposed nothing. These maps are
// host-scoped so a legitimate page genuinely titled "Instagram" elsewhere is not
// affected, and they are compared after whitespace normalization and lowercasing.

var genericPageTitles = map[string]struct{}{
	"page not found": {},
	"404 not found":  {},
	"404":            {},
	"please wait":    {},
	"please wait...": {},
}

var tiktokGenericTitles = map[string]struct{}{
	"tiktok":                  {},
	"tiktok - make your day":  {},
	"tiktok - 404":            {},
	"tiktok - page not found": {},
}

var instagramGenericTitles = map[string]struct{}{
	"instagram":          {},
	"instagram - log in": {},
	"login • instagram":  {},
	"log in • instagram": {},
	"instagram login":    {},
}

var facebookGenericTitles = map[string]struct{}{
	"facebook":                     {},
	"log in to facebook":           {},
	"facebook - log in or sign up": {},
}

var xGenericTitles = map[string]struct{}{
	"x":                 {},
	"twitter":           {},
	"log in to x":       {},
	"log in to twitter": {},
}

var youtubeGenericTitles = map[string]struct{}{
	"youtube": {},
}

var amazonGenericTitles = map[string]struct{}{
	"amazon":       {},
	"amazon.com":   {},
	"amazon.co.uk": {},
	"amazon.de":    {},
	"amazon.sg":    {},
	"amazon.co.jp": {},
}

var genericTitlesByHost = map[string]map[string]struct{}{
	"tiktok.com":     tiktokGenericTitles,
	"www.tiktok.com": tiktokGenericTitles,
	"m.tiktok.com":   tiktokGenericTitles,
	"vm.tiktok.com":  tiktokGenericTitles,
	"vt.tiktok.com":  tiktokGenericTitles,

	"instagram.com":     instagramGenericTitles,
	"www.instagram.com": instagramGenericTitles,

	"facebook.com":     facebookGenericTitles,
	"www.facebook.com": facebookGenericTitles,
	"m.facebook.com":   facebookGenericTitles,

	"x.com":              xGenericTitles,
	"www.x.com":          xGenericTitles,
	"twitter.com":        xGenericTitles,
	"www.twitter.com":    xGenericTitles,
	"mobile.twitter.com": xGenericTitles,

	"youtube.com":     youtubeGenericTitles,
	"www.youtube.com": youtubeGenericTitles,
	"m.youtube.com":   youtubeGenericTitles,
	"youtu.be":        youtubeGenericTitles,
	"www.youtu.be":    youtubeGenericTitles,

	"amazon.com":       amazonGenericTitles,
	"www.amazon.com":   amazonGenericTitles,
	"amazon.co.uk":     amazonGenericTitles,
	"www.amazon.co.uk": amazonGenericTitles,
	"amazon.de":        amazonGenericTitles,
	"www.amazon.de":    amazonGenericTitles,
	"amazon.sg":        amazonGenericTitles,
	"www.amazon.sg":    amazonGenericTitles,
	"amazon.co.jp":     amazonGenericTitles,
	"www.amazon.co.jp": amazonGenericTitles,
}

// dropGenericTitle removes a title that is only the page's shell.
//
// It is applied to every source, including the oEmbed response, because a
// provider can publish a generic title too. Removing it before the source
// selection is what lets a fallback source supply the real title.
func dropGenericTitle(
	pageURL *url.URL,
	metadata Metadata,
) Metadata {
	if metadata.Title == nil || pageURL == nil {
		return metadata
	}

	title := strings.ToLower(normalizeText(*metadata.Title))
	if title == "" {
		return metadata
	}

	if _, generic := genericPageTitles[title]; generic {
		metadata.Title = nil

		return metadata
	}

	titles, ok := genericTitlesByHost[normalizeHostname(pageURL.Hostname())]
	if !ok {
		return metadata
	}

	if _, generic := titles[title]; generic {
		metadata.Title = nil
	}

	return metadata
}
