package enrichment

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"
)

// Amazon product pages.
//
// Amazon answers a datacenter address with the full product page, but publishes
// no Open Graph metadata and no JSON-LD: the title and description are the
// document title and the standard meta description, and the preview image exists
// only as attributes on the main product image element. That image is read here
// and nowhere else, so the generic extractor stays free of product-page markup.
//
// Only the host is used to decide whether to read those attributes. The platform
// is still whatever the page declares, and Amazon declares none, so an Amazon item
// continues to enrich without a platform (decision 3).

// productImageAttributes is what the extractor keeps from a product image
// element. Only the attributes a preview can be built from are stored.
type productImageAttributes struct {
	oldHires     string
	dynamicImage string
}

// amazonHosts are the hostnames whose product pages use the landing-image
// attributes this package reads.
var amazonHosts = map[string]struct{}{
	"amazon.com":       {},
	"www.amazon.com":   {},
	"amazon.co.uk":     {},
	"www.amazon.co.uk": {},
	"amazon.de":        {},
	"www.amazon.de":    {},
	"amazon.sg":        {},
	"www.amazon.sg":    {},
	"amazon.co.jp":     {},
	"www.amazon.co.jp": {},
}

func isAmazonHost(hostname string) bool {
	_, ok := amazonHosts[normalizeHostname(hostname)]

	return ok
}

// amazonProductImagePath is the path segment Amazon uses for product photos.
//
// It separates a product photo from the other images on the page. A page can
// answer with a share preview instead of the product, and its landing image is a
// brand icon under /images/G/; storing that as the item's preview would be worse
// than storing no image at all.
const amazonProductImagePath = "/images/I/"

func isAmazonProductImageURL(raw string) bool {
	return strings.Contains(raw, amazonProductImagePath)
}

// firstDynamicImageURL picks the largest image from an Amazon
// data-a-dynamic-image attribute.
//
// The attribute is a JSON object mapping each candidate URL to its [width,
// height] pair. The largest product image is chosen because the attribute exists
// precisely to let a client pick a size, and a preview wants the sharpest one.
// Non-product images are skipped by the same rule the single-URL attribute uses.
func firstDynamicImageURL(raw string) string {
	if raw == "" {
		return ""
	}

	var images map[string][]int
	if err := json.Unmarshal([]byte(raw), &images); err != nil {
		return ""
	}

	type candidate struct {
		url   string
		width int
	}

	candidates := make([]candidate, 0, len(images))

	for imageURL, dimensions := range images {
		if !isAmazonProductImageURL(imageURL) {
			continue
		}

		width := 0
		if len(dimensions) > 0 {
			width = dimensions[0]
		}

		candidates = append(candidates, candidate{url: imageURL, width: width})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].width > candidates[j].width
	})

	if len(candidates) == 0 {
		return ""
	}

	return candidates[0].url
}

// amazonProductImage returns the preview image an Amazon product page published.
func amazonProductImage(doc *document) string {
	if doc == nil {
		return ""
	}

	image, ok := doc.productImages["landingImage"]
	if !ok {
		return ""
	}

	if image.oldHires != "" && isAmazonProductImageURL(image.oldHires) {
		return image.oldHires
	}

	return firstDynamicImageURL(image.dynamicImage)
}

// applyAmazonProductImage fills the preview image from the landing-image
// attributes when the page published no metadata image of its own.
func applyAmazonProductImage(
	doc *document,
	pageURL *url.URL,
	metadata Metadata,
) Metadata {
	if doc == nil || pageURL == nil || !isAmazonHost(pageURL.Hostname()) {
		return metadata
	}

	if metadata.ImageURL != nil {
		return metadata
	}

	image := amazonProductImage(doc)
	if image == "" {
		return metadata
	}

	metadata.ImageURL = optionalURL(image, pageURL)

	return metadata
}
