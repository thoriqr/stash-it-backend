package operator

import (
	"embed"
	"html/template"
	"io"
)

// templates holds the page's HTML, compiled into the binary.
//
// Embedding rather than reading from disk is what keeps the deploy a single file: the
// server has no working directory to depend on and no template that can go missing
// between a build and a run.
//
//go:embed templates/*.html
var templates embed.FS

// parsedTemplates is the template set, parsed once at startup.
//
// Parsing at startup rather than per request means a malformed template is a startup
// failure rather than a 500 on the first request that happens to render it.
var parsedTemplates = template.Must(
	template.New("operator").ParseFS(templates, "templates/*.html"),
)

// render writes one named template to w.
//
// The templates are executed directly to the response writer rather than buffered
// first. A template error would therefore leave a partial page, which is acceptable
// for a page read by one person on their own machine: the alternative is buffering
// every page in memory to protect a reader who can simply reload.
func render(w io.Writer, name string, data any) error {
	return parsedTemplates.ExecuteTemplate(w, name, data)
}
