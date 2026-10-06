package enrichment

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeText(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		expected string
	}{
		{"unchanged", "A title", "A title"},
		{"trimmed", "  A title  ", "A title"},
		{"newlines collapsed", "A\ntitle", "A title"},
		{"tabs collapsed", "A\ttitle", "A title"},
		{"runs collapsed", "A     title", "A title"},
		{"mixed whitespace", " \n\t A \r\n title \t ", "A title"},
		{"non breaking space folded", "A\u00a0title", "A title"},
		{"empty stays empty", "", ""},
		{"whitespace only becomes empty", " \n\t ", ""},
		{"punctuation preserved", "Why Go's \"Fuzzing\" Works — A Study!",
			"Why Go's \"Fuzzing\" Works — A Study!"},
		{"case preserved", "An Interesting Article", "An Interesting Article"},
		{"inner punctuation untouched", "Item 3: a study (2026)",
			"Item 3: a study (2026)"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, normalizeText(tt.value))
		})
	}
}

func TestNormalizeURL(t *testing.T) {
	base, err := url.Parse("https://example.com/articles/1")
	require.NoError(t, err)

	// A genuinely absent base, which is the case with no base at all.
	var nilBase *url.URL

	cases := []struct {
		name     string
		value    string
		base     *url.URL
		expected string
	}{
		{
			name:     "an absolute url is kept",
			value:    "https://other.example/x.png",
			base:     base,
			expected: "https://other.example/x.png",
		},
		{
			name:     "a root relative url resolves against the host",
			value:    "/static/og.png",
			base:     base,
			expected: "https://example.com/static/og.png",
		},
		{
			name:     "a document relative url resolves against the directory",
			value:    "og.png",
			base:     base,
			expected: "https://example.com/articles/og.png",
		},
		{
			name:     "a parent relative url resolves upward",
			value:    "../og.png",
			base:     base,
			expected: "https://example.com/og.png",
		},
		{
			name:     "a protocol relative url adopts the base scheme",
			value:    "//cdn.example/og.png",
			base:     base,
			expected: "https://cdn.example/og.png",
		},
		{
			name:     "surrounding whitespace is trimmed",
			value:    "  /og.png  ",
			base:     base,
			expected: "https://example.com/og.png",
		},
		{
			name:     "a query string survives",
			value:    "/og.png?v=2",
			base:     base,
			expected: "https://example.com/og.png?v=2",
		},
		{
			name:     "a javascript url is dropped",
			value:    "javascript:alert(1)",
			base:     base,
			expected: "",
		},
		{
			name:     "a data url is dropped",
			value:    "data:image/png;base64,AAAA",
			base:     base,
			expected: "",
		},
		{
			name:     "a file url is dropped",
			value:    "file:///etc/passwd",
			base:     base,
			expected: "",
		},
		{
			name:     "an empty value is dropped",
			value:    "   ",
			base:     base,
			expected: "",
		},
		{
			name:     "a relative url with no base is dropped",
			value:    "/og.png",
			base:     nilBase,
			expected: "",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, normalizeURL(tt.value, tt.base))
		})
	}
}

func TestNormalizeURL_NoBaseKeepsAbsoluteValues(t *testing.T) {
	// Without a base there is nothing to resolve against, but an absolute value
	// is already complete and is still usable.
	require.Equal(
		t,
		"https://example.com/og.png",
		normalizeURL("https://example.com/og.png", nil),
	)
}

func TestOptionalText(t *testing.T) {
	t.Run("a value becomes a pointer", func(t *testing.T) {
		value := optionalText("  A title  ")

		require.NotNil(t, value)
		require.Equal(t, "A title", *value)
	})

	t.Run("a blank value becomes nil", func(t *testing.T) {
		// "No description" has to stay distinguishable from "an empty one", so a
		// blank never becomes a pointer to "".
		require.Nil(t, optionalText("   "))
		require.Nil(t, optionalText(""))
	})
}

func TestOptionalURL(t *testing.T) {
	base, err := url.Parse("https://example.com/articles/1")
	require.NoError(t, err)

	t.Run("a resolvable value becomes a pointer", func(t *testing.T) {
		value := optionalURL("/og.png", base)

		require.NotNil(t, value)
		require.Equal(t, "https://example.com/og.png", *value)
	})

	t.Run("an unresolvable value becomes nil", func(t *testing.T) {
		require.Nil(t, optionalURL("javascript:alert(1)", base))
		require.Nil(t, optionalURL("", base))
	})
}

func TestParseMediaType(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		expected    string
	}{
		{"plain html", "text/html", "text/html"},
		{"with charset", "text/html; charset=utf-8", "text/html"},
		{"with no space", "text/html;charset=utf-8", "text/html"},
		{"upper case", "TEXT/HTML", "text/html"},
		{"xhtml", "application/xhtml+xml", "application/xhtml+xml"},
		{"an image", "image/png", "image/png"},
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{
			name:        "a malformed header falls back to a manual split",
			contentType: "text/html; charset",
			expected:    "text/html",
		},
		{
			name:        "a malformed header with no semicolon",
			contentType: "!!!not a media type!!!",
			expected:    "!!!not a media type!!!",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, parseMediaType(tt.contentType))
		})
	}
}

func TestPrepareHTMLBody(t *testing.T) {
	t.Run("a declared html type is accepted unread", func(t *testing.T) {
		reader, err := prepareHTMLBody(
			strings.NewReader("<html></html>"),
			"text/html",
		)

		require.NoError(t, err)
		require.NotNil(t, reader)
	})

	t.Run("a sniffer that accepts restores the whole document", func(t *testing.T) {
		const document = "<!doctype html><html><head>" +
			`<meta property="og:title" content="Restored">` +
			"</head></html>"

		reader, err := prepareHTMLBody(
			strings.NewReader(document),
			"application/octet-stream",
		)

		require.NoError(t, err)

		// The sniffed prefix has to come back, or the parser would lose the
		// opening tags the sniff just consumed.
		read, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.Equal(t, document, string(read))
	})

	t.Run("a body that does not sniff as html is refused", func(t *testing.T) {
		reader, err := prepareHTMLBody(
			strings.NewReader("\x89PNG\r\n\x1a\n not html"),
			"application/octet-stream",
		)

		require.ErrorIs(t, err, ErrUnsupportedContentType)
		require.Nil(t, reader)
	})

	t.Run("an empty body is refused", func(t *testing.T) {
		reader, err := prepareHTMLBody(
			strings.NewReader(""),
			"",
		)

		require.ErrorIs(t, err, ErrUnsupportedContentType)
		require.Nil(t, reader)
	})

	t.Run("a short body is sniffed in full", func(t *testing.T) {
		reader, err := prepareHTMLBody(
			strings.NewReader("<p>hi"),
			"",
		)

		// "<p" is not one of the document markers, so this is refused, which
		// also proves the short-read path is exercised without error.
		require.ErrorIs(t, err, ErrUnsupportedContentType)
		require.Nil(t, reader)
	})
}

func TestIsHTMLPrefix(t *testing.T) {
	cases := []struct {
		name     string
		prefix   string
		expected bool
	}{
		{"doctype", "<!DOCTYPE html><html>", true},
		{"html tag", "<html lang=\"en\">", true},
		{"head tag", "<head>", true},
		{"body tag", "<body>", true},
		{"title tag", "<title>x</title>", true},
		{"meta tag", "<meta charset=\"utf-8\">", true},
		{"script tag", "<script type=\"application/ld+json\">", true},
		{"xml declaration", "<?xml version=\"1.0\"?>", true},
		{"leading whitespace then html", "\n\n  <html>", true},
		{"empty", "", false},
		{"plain text", "just some words", false},
		{"a binary payload", "\x00\x01\x02\x03", false},
		{"a pdf header", "%PDF-1.7\n%\xe2\xe3", false},
		{"a zip header", "PK\x03\x04\x14\x00", false},
		{"json", `{"@type":"Article"}`, false},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(
				t,
				tt.expected,
				isHTMLPrefix([]byte(tt.prefix)),
			)
		})
	}
}

func TestReadSniffPrefix(t *testing.T) {
	t.Run("reads up to the window", func(t *testing.T) {
		prefix, err := readSniffPrefix(strings.NewReader("abcdef"))

		require.NoError(t, err)
		require.Equal(t, "abcdef", string(prefix))
	})

	t.Run("a long body is capped", func(t *testing.T) {
		prefix, err := readSniffPrefix(
			strings.NewReader(strings.Repeat("x", 4096)),
		)

		require.NoError(t, err)
		require.Len(t, prefix, sniffLength)
	})

	t.Run("an empty body is not an error", func(t *testing.T) {
		prefix, err := readSniffPrefix(strings.NewReader(""))

		require.NoError(t, err)
		require.Empty(t, prefix)
	})

	t.Run("a reader failure is reported", func(t *testing.T) {
		_, err := readSniffPrefix(failingReader{})

		require.Error(t, err)
	})
}

func TestFailureError(t *testing.T) {
	// Error must reach the wrapped sentinel, since that is what makes the failure
	// readable in a log without unwrapping it first.
	failure := newFailure(FailureContent, ErrUnexpectedStatus)

	require.Equal(t, ErrUnexpectedStatus.Error(), failure.Error())
	require.ErrorIs(t, failure, ErrUnexpectedStatus)
}

func TestResponseURL(t *testing.T) {
	t.Run("the response request is used", func(t *testing.T) {
		redirected, err := url.Parse("https://example.com/final")
		require.NoError(t, err)

		response := &http.Response{
			Request: &http.Request{URL: redirected},
		}

		require.Equal(t, redirected, responseURL(response))
	})

	t.Run("no request yields nothing", func(t *testing.T) {
		// A RoundTripper is permitted to leave Request unset. Returning nothing
		// must not panic, and resolveMetadataURL already copes with a nil base.
		require.Nil(t, responseURL(&http.Response{}))
	})
}

// failingReader always fails, standing in for a connection that drops mid body.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("connection reset")
}
