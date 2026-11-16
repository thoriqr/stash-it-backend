package enrichment

import (
	"encoding/json"
	"net/url"
	"strings"
)

// TikTok's watch pages do not publish Open Graph metadata. The caption, author
// and cover are delivered as a JSON state blob instead, under a script element
// with a fixed id. That blob is not HTML metadata, so it is captured separately
// and read only for the hosts it belongs to.

// tiktokUniversalDataScriptID is the id of the state blob TikTok renders its
// video page from.
const tiktokUniversalDataScriptID = "__UNIVERSAL_DATA_FOR_REHYDRATION__"

// tiktokHosts are the hostnames whose pages carry the universal data blob.
//
// The short-link hosts are included because the page they redirect to is the same
// watch page, and the final URL is what the metadata is read from.
var tiktokHosts = map[string]struct{}{
	"tiktok.com":     {},
	"www.tiktok.com": {},
	"m.tiktok.com":   {},
	"vm.tiktok.com":  {},
	"vt.tiktok.com":  {},
}

func isTikTokHost(hostname string) bool {
	_, ok := tiktokHosts[normalizeHostname(hostname)]

	return ok
}

// tiktokUniversalData is the part of the state blob this package reads.
type tiktokUniversalData struct {
	DefaultScope struct {
		VideoDetail tiktokVideoDetail `json:"webapp.video-detail"`
	} `json:"__DEFAULT_SCOPE__"`
}

// tiktokVideoDetail is the video entity inside the state blob.
type tiktokVideoDetail struct {
	ItemInfo struct {
		ItemStruct struct {
			Desc  string `json:"desc"`
			Video struct {
				Cover string `json:"cover"`
			} `json:"video"`
		} `json:"itemStruct"`
	} `json:"itemInfo"`
}

// tiktokMetadataFromUniversalData reads the fields this package can store.
//
// The cover URL is signed and expires, so it is reported as the image the page
// published rather than as a permanent asset. Persisting it is the caller's
// decision; a later enrichment reads a fresh URL.
func tiktokMetadataFromUniversalData(raw string) Metadata {
	if strings.TrimSpace(raw) == "" {
		return Metadata{}
	}

	var root tiktokUniversalData
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return Metadata{}
	}

	item := root.DefaultScope.VideoDetail.ItemInfo.ItemStruct

	return Metadata{
		Title:    optionalText(item.Desc),
		ImageURL: optionalURL(item.Video.Cover, nil),
	}
}

// applyTikTokUniversalData replaces the generic document title with the caption
// TikTok renders from its state blob.
//
// The replacement is deliberate. The document title on a watch page is the
// product's shell title ("TikTok - Make Your Day"), which is never the video's
// title; the caption is. An absent caption leaves whatever the HTML supplied.
func applyTikTokUniversalData(
	doc *document,
	pageURL *url.URL,
	metadata Metadata,
) Metadata {
	if doc == nil || pageURL == nil || !isTikTokHost(pageURL.Hostname()) {
		return metadata
	}

	raw := doc.scriptPayloads[tiktokUniversalDataScriptID]
	if raw == "" {
		return metadata
	}

	discovered := tiktokMetadataFromUniversalData(raw)

	if discovered.Title != nil {
		metadata.Title = discovered.Title
	}

	if discovered.ImageURL != nil {
		metadata.ImageURL = discovered.ImageURL
	}

	return metadata
}

// isTikTokNotFoundURL reports whether a page ended up on TikTok's not-found
// route.
//
// An expired short link redirects there while answering 200, so the status alone
// does not reveal it. The page would otherwise be recorded as a successful
// enrichment with the generic "TikTok" title.
func isTikTokNotFoundURL(pageURL *url.URL) bool {
	if pageURL == nil || !isTikTokHost(pageURL.Hostname()) {
		return false
	}

	path := strings.TrimSuffix(pageURL.Path, "/")

	return path == "/404" || strings.HasPrefix(path, "/404/")
}
