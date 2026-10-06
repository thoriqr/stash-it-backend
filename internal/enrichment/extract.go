package enrichment

import (
	"encoding/json"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// document is everything the extractor found on one page, before any selection
// has been applied.
//
// Candidates are keyed by the constant names in constants.go, whether they came
// from a meta tag, the title element, a canonical link, or a JSON-LD block. Flattening
// all four sources into one map is what lets precedence be expressed as data
// rather than as a chain of conditionals, and it means a JSON-LD value and a
// meta tag value compete on the same footing.
type document struct {
	candidates map[string]string
}

// set records a candidate value.
//
// The first non-empty value for a key wins. A page that declares the same meta
// tag twice is malformed, but it happens, and taking the first declaration is what
// every mainstream extractor does, so later duplicates are ignored rather than
// allowed to silently override an earlier, better formed value.
func (d *document) set(key, value string) {
	if key == "" {
		return
	}

	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return
	}

	if _, exists := d.candidates[key]; exists {
		return
	}

	d.candidates[key] = trimmed
}

// extract parses an HTML document and returns everything it declares.
//
// A parse failure is reported, but only when nothing at all could be read. HTML
// in the wild is rarely well formed and x/net/html recovers from the great
// majority of it, so a partially parseable page yields whatever was found before
// the problem rather than being discarded.
func extract(
	reader io.Reader,
	baseURL *url.URL,
) (*document, error) {
	doc := &document{
		candidates: make(map[string]string),
	}

	tokenizer := html.NewTokenizer(reader)

	var (
		inTitle      bool
		titleBuilder strings.Builder
		inJSONLD     bool
		jsonBuilder  strings.Builder
	)

	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			// io.EOF is the normal end of the document. Any other error means
			// the reader failed, which is a genuine parse failure worth
			// reporting.
			err := tokenizer.Err()
			if err == io.EOF {
				return doc, nil
			}

			return nil, err

		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokenizer.Token()

			switch token.Data {
			case "title":
				// Accumulated title text is only committed on a closing tag, and
				// never at end of input. <title> is an RCDATA element, so the
				// tokenizer reads to end of file looking for its close and emits
				// everything after it, markup included, as title text. Committing
				// that would store the entire rest of a malformed document as the
				// title, which is worse than storing nothing at all.
				if !inTitle {
					inTitle = true
					titleBuilder.Reset()
				}

			case "script":
				inJSONLD = isJSONLDScript(token.Attr)
				if inJSONLD {
					jsonBuilder.Reset()
				}

			case "meta":
				collectMeta(doc, token.Attr)

			case "link":
				collectLink(doc, token.Attr)
			}

		case html.EndTagToken:
			token := tokenizer.Token()

			switch token.Data {
			case "title":
				if inTitle {
					inTitle = false
					doc.set(titleElement, titleBuilder.String())
					titleBuilder.Reset()
				}

			case "script":
				if inJSONLD {
					inJSONLD = false
					collectJSONLD(doc, jsonBuilder.String())
					jsonBuilder.Reset()
				}
			}

		case html.TextToken:
			// Text inside <title> is the title. Text inside a JSON-LD script is
			// raw JSON and must not be unescaped, which is what the tokenizer
			// provides for a raw text element.
			switch {
			case inTitle:
				_, _ = titleBuilder.Write(tokenizer.Text())

			case inJSONLD:
				_, _ = jsonBuilder.Write(tokenizer.Text())
			}
		}
	}
}

// isJSONLDScript reports whether a script element carries JSON-LD.
//
// The type attribute may carry a charset or other parameters, and is compared
// only up to the first semicolon so that a declared encoding does not hide the
// type.
func isJSONLDScript(attrs []html.Attribute) bool {
	for _, attr := range attrs {
		if attr.Key != "type" {
			continue
		}

		value := strings.ToLower(strings.TrimSpace(attr.Val))

		if index := strings.Index(value, ";"); index >= 0 {
			value = strings.TrimSpace(value[:index])
		}

		return value == scriptTypeJSONLD
	}

	return false
}

// collectMeta records a meta tag.
//
// Open Graph uses property while everything else uses name, and pages mix the two
// freely, so both attributes are accepted for every tag. The value is lowercased
// for lookup because the vocabularies are case insensitive in practice even though
// they are written in lower case by convention.
func collectMeta(
	doc *document,
	attrs []html.Attribute,
) {
	key := ""

	for _, attr := range attrs {
		switch attr.Key {
		case "name", "property", "itemprop":
			key = strings.ToLower(strings.TrimSpace(attr.Val))
		}
	}

	if key == "" {
		return
	}

	for _, attr := range attrs {
		if attr.Key != "content" {
			continue
		}

		doc.set(key, attr.Val)

		return
	}
}

// collectLink records a canonical link.
//
// rel carries a space separated list of tokens, so it is searched for the
// canonical token rather than compared against. Only href is of interest, and
// only for a canonical relation.
func collectLink(
	doc *document,
	attrs []html.Attribute,
) {
	isCanonical := false

	for _, attr := range attrs {
		if attr.Key != "rel" {
			continue
		}

		for token := range strings.FieldsSeq(strings.ToLower(attr.Val)) {
			if token == linkRelCanonical {
				isCanonical = true

				break
			}
		}
	}

	if !isCanonical {
		return
	}

	for _, attr := range attrs {
		if attr.Key == "href" {
			doc.set(metaLinkCanonical, attr.Val)

			return
		}
	}
}

// collectJSONLD folds one JSON-LD block into the candidate map.
//
// A block that does not parse is discarded and the rest of the page carries on.
// JSON-LD is authored by hand and by templating engines, so it is the single most
// common source of malformed markup on an otherwise fine page, and letting one
// broken block cost a page its Open Graph tags would make enrichment far too
// brittle to be useful.
func collectJSONLD(
	doc *document,
	raw string,
) {
	if strings.TrimSpace(raw) == "" {
		return
	}

	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return
	}

	walkJSONLD(doc, decoded, false)
}

// jsonLDReferenceKeys are the properties whose object is a reference to another
// entity rather than a statement about this page.
//
// Descending into one of these and collecting its name or description as if it
// described the page is a real and easy mistake to make: publisher.name in
// particular is on a large share of pages and would otherwise win the title
// precedence against the actual article headline.
//
// Organization and WebSite are absent because they are recognised by their @type
// rather than by the property they hang off, and an object carrying one of those
// types is handled before this map is ever consulted.
var jsonLDReferenceKeys = map[string]struct{}{
	jsonPropPublisher: {},
	jsonPropAuthor:    {},
	jsonPropImage:     {},
}

// walkJSONLD descends a decoded JSON-LD value in document order.
//
// reference is set once the walk has entered a property that names another
// entity, and suppresses collection of the generic page fields from everything
// below it. Those subtrees are still walked, because that is how a publisher's
// name or an ImageObject's url is found, but through a purpose built accessor
// rather than the generic one.
func walkJSONLD(
	doc *document,
	node any,
	reference bool,
) {
	switch value := node.(type) {
	case map[string]any:
		walkJSONLDObject(doc, value, reference)

	case []any:
		for _, item := range value {
			walkJSONLD(doc, item, reference)
		}
	}
}

func walkJSONLDObject(
	doc *document,
	object map[string]any,
	reference bool,
) {
	// @type is read before anything else because it decides how the rest of the
	// object is treated. An object typed as an Organization or a WebSite is a
	// statement about the site rather than about the page, so its name is
	// recorded against the platform candidates and its generic fields are not
	// collected at all.
	switch normalizeJSONLDType(object) {
	case jsonTypeOrganization:
		doc.set(jsonLDOrganizationName, jsonLDString(object, jsonPropName))
		return

	case jsonTypeWebSite:
		doc.set(jsonLDWebSiteName, jsonLDString(object, jsonPropName))
		return
	}

	if !reference {
		doc.set(jsonLDHeadline, jsonLDString(object, jsonPropHeadline))
		doc.set(jsonLDDescription, jsonLDString(object, jsonPropDescription))
		doc.set(jsonLDName, jsonLDString(object, jsonPropName))
		doc.set(jsonLDURL, jsonLDString(object, jsonPropURL))
		doc.set(jsonLDImage, jsonLDImageURL(object))

		// publisher and author are read here rather than being descended into
		// generically, so that their name becomes a platform or author candidate
		// rather than a candidate for the page title.
		doc.set(jsonLDPublisherName, jsonLDNestedName(object, jsonPropPublisher))
		doc.set(jsonLDAuthorName, jsonLDNestedName(object, jsonPropAuthor))
	}

	// @graph holds the page's entities as a list, and is by far the most common
	// container once a site uses JSON-LD at all.
	if graph, ok := object[jsonPropGraph]; ok {
		walkJSONLD(doc, graph, reference)
	}

	// Any remaining object-valued property may still contain a nested entity
	// worth reading, such as a list of related articles. Descending with
	// reference set keeps such an entity's name from being mistaken for the
	// page's title.
	for key, value := range object {
		if _, isReference := jsonLDReferenceKeys[key]; isReference {
			continue
		}

		if key == jsonPropGraph {
			continue
		}

		if _, isMapOrSlice := value.(map[string]any); isMapOrSlice {
			walkJSONLD(doc, value, true)
		}
	}
}

// normalizeJSONLDType reduces a JSON-LD @type to its final segment, lowercased.
//
// The value may be a string, a list of strings, or the @id form of a type, and
// pages write it as "Organization", "schema:Organization" and
// "https://schema.org/Organization" interchangeably. Only the last segment is
// compared, so all three forms resolve to the same type.
func normalizeJSONLDType(object map[string]any) string {
	switch value := object[jsonPropType].(type) {
	case string:
		return jsonLDTypeName(value)

	case []any:
		for _, item := range value {
			name, ok := item.(string)
			if !ok {
				continue
			}

			if normalized := jsonLDTypeName(name); normalized != "" {
				return normalized
			}
		}
	}

	return ""
}

// jsonLDTypeName extracts the final segment of a possibly qualified type name.
func jsonLDTypeName(raw string) string {
	name := strings.TrimSpace(raw)
	if name == "" {
		return ""
	}

	if index := strings.LastIndexAny(name, "/#:"); index >= 0 {
		name = name[index+1:]
	}

	return strings.ToLower(strings.TrimSpace(name))
}

// jsonLDString reads a scalar property as a string.
//
// JSON-LD allows a plain string, and also allows the {"@value": "..."} form for
// a typed value, so both are accepted. Anything else, such as an object or a
// list, yields nothing rather than being coerced into something meaningless.
func jsonLDString(
	object map[string]any,
	key string,
) string {
	switch value := object[key].(type) {
	case string:
		return value

	case map[string]any:
		if inner, ok := value[jsonPropValue].(string); ok {
			return inner
		}
	}

	return ""
}

// jsonLDNestedName reads the name of an entity referenced by a property, such as
// publisher or author.
//
// The reference may be the name itself as a bare string, or an object carrying a
// name, and either form is common.
func jsonLDNestedName(
	object map[string]any,
	key string,
) string {
	switch value := object[key].(type) {
	case string:
		return value

	case map[string]any:
		return jsonLDString(value, jsonPropName)

	case []any:
		for _, item := range value {
			nested, ok := item.(map[string]any)
			if !ok {
				continue
			}

			if name := jsonLDString(nested, jsonPropName); name != "" {
				return name
			}
		}
	}

	return ""
}

// jsonLDImageURL reads an image property.
//
// The shape varies more than any other JSON-LD property: a bare URL string, an
// ImageObject carrying a url or a contentUrl, or a list of any of those. The first
// usable entry wins, since an image list is usually ordered by preference.
func jsonLDImageURL(object map[string]any) string {
	switch value := object[jsonPropImage].(type) {
	case string:
		return value

	case map[string]any:
		return firstNonEmpty(
			jsonLDString(value, jsonPropURL),
			jsonLDString(value, jsonPropContentURL),
		)

	case []any:
		for _, item := range value {
			switch entry := item.(type) {
			case string:
				if strings.TrimSpace(entry) != "" {
					return entry
				}

			case map[string]any:
				if found := firstNonEmpty(
					jsonLDString(entry, jsonPropURL),
					jsonLDString(entry, jsonPropContentURL),
				); found != "" {
					return found
				}
			}
		}
	}

	return ""
}

// firstNonEmpty returns the first value that is not blank after trimming.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}
