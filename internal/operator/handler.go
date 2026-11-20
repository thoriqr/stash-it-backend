package operator

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type Handler struct {
	service Service
	secret  string
}

func NewHandler(service Service, secret string) *Handler {
	return &Handler{service: service, secret: secret}
}

// Index renders one page of reports.
func (h *Handler) Index(c fiber.Ctx) error {
	limit := intQuery(c, "limit", DefaultLimit)
	page := intQuery(c, "page", 1)
	if page < 1 {
		page = 1
	}

	result, err := h.service.List(c.Context(), ListParams{
		Limit:  limit,
		Offset: (page - 1) * limit,
		Status: c.Query("status"),
	})
	if err != nil {
		return h.renderError(c, err)
	}

	// Read separately from the listing, and deliberately not built from it: the summary
	// answers questions about the deployment, so it must not change when the operator
	// filters or pages the reports.
	summary, err := h.service.Summary(c.Context())
	if err != nil {
		return h.renderError(c, err)
	}

	notice := noticeFor("", "")

	switch c.Query("notice") {
	case "status":
		notice = noticeFor("Status updated.", noticeSuccess)
	case "deleted":
		notice = noticeFor("Report deleted.", noticeSuccess)
	case "notfound":
		notice = noticeFor("That report is no longer there. The list was reloaded.", noticeFailure)
	case "invalid":
		notice = noticeFor("That status is not one this page can set.", noticeFailure)
	}

	c.Type("html", "utf-8")

	return render(c.Response().BodyWriter(), "index.html", newPageData(result, summary, notice))
}

// SetStatus moves one report to another status and redirects back to the list.
func (h *Handler) SetStatus(c fiber.Ctx) error {
	feedbackID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return h.renderError(c, err)
	}

	status := c.FormValue("status")

	if _, err := h.service.SetStatus(c.Context(), feedbackID, status); err != nil {
		return h.redirectWithNotice(c, noticeForError(err))
	}

	return h.redirectWithNotice(c, "status")
}

// Delete removes one report and redirects back to the list.
func (h *Handler) Delete(c fiber.Ctx) error {
	feedbackID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return h.renderError(c, err)
	}

	deleted, err := h.service.Delete(c.Context(), feedbackID)
	if err != nil {
		return h.renderError(c, err)
	}

	if !deleted {
		return h.redirectWithNotice(c, "notfound")
	}

	return h.redirectWithNotice(c, "deleted")
}

// redirectWithNotice sends the browser back to the list it came from.
//
// The page and filter travel with the redirect so acting on a report does not lose the
// reader's place: closing something on page 3 should return to page 3 of the same
// filter, not to the top of everything.
//
// 303 rather than 302 is the semantic choice: it says "the POST is finished, fetch
// this with GET", which is also what stops a browser re-posting when the operator
// refreshes.
func (h *Handler) redirectWithNotice(c fiber.Ctx, notice string) error {
	query := "notice=" + notice

	if page := c.FormValue("page"); page != "" {
		query += "&page=" + page
	}

	if status := c.FormValue("status_filter"); status != "" {
		query += "&status=" + status
	}

	return c.Redirect().Status(fiber.StatusSeeOther).To("/?" + query)
}

// noticeForError maps a service error onto the notice a redirect carries.
func noticeForError(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "notfound"
	case errors.Is(err, ErrUnknownStatus):
		return "invalid"
	default:
		return "invalid"
	}
}

// renderError renders a failure as a page rather than as JSON.
//
// This is an HTML surface: a browser has no use for the API's error envelope, so a
// failure becomes a small readable page with the reason on it.
func (h *Handler) renderError(c fiber.Ctx, err error) error {
	c.Status(http.StatusInternalServerError)
	c.Type("html", "utf-8")

	return render(c.Response().BodyWriter(), "index.html", pageData{
		Notice:     "Something went wrong: " + err.Error(),
		NoticeKind: noticeFailure,
		Now:        Now(),
	})
}

// intQuery reads an integer query parameter, falling back to fallback.
//
// An unparsable value is treated as absent rather than as an error: this is a page, and
// a browser that sent something strange should get the default rather than a failure it
// cannot act on.
func intQuery(c fiber.Ctx, name string, fallback int) int {
	raw := c.Query(name)
	if raw == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 1 {
		return fallback
	}

	return parsed
}

// Guard refuses a request that did not come from this page.
//
// Three properties hold this surface closed, and this is the third:
//
//  1. It is bound to loopback, so the network cannot reach it.
//  2. It has no route on the public app, so Caddy never forwards to it.
//  3. Every mutating request must carry this page's own origin, so a website the
//     operator visits cannot make their browser post to it. `Origin` is a forbidden
//     header name for JavaScript, so a hostile page cannot forge it.
//
// The secret is optional and is the fourth: a deployment that binds to a reachable
// address by mistake still requires it. An empty secret means the check is skipped,
// which is the ordinary local case.
func (h *Handler) Guard(c fiber.Ctx) error {
	if c.Method() == http.MethodPost {
		if !h.sameOrigin(c) {
			return c.Status(http.StatusForbidden).SendString(
				"forbidden: this form must be submitted from the operator page",
			)
		}
	}

	if h.secret != "" {
		presented := c.Get(SecretHeader)

		if subtle.ConstantTimeCompare(
			[]byte(presented),
			[]byte(h.secret),
		) != 1 {
			return c.Status(http.StatusUnauthorized).SendString(
				"unauthorized: missing or invalid operator secret",
			)
		}
	}

	return c.Next()
}

// sameOrigin reports whether a POST came from this page.
//
// The comparison is against the Host the request actually arrived on, not against a
// configured value, so the check works on whatever address the page is bound to:
// loopback in production, a LAN address in development, and a forwarded port over SSH.
//
// Only the authority is compared, not the scheme. The page is served over plain HTTP
// everywhere it runs — it is reachable only from the machine it is on — and comparing the
// scheme would make the check fail the moment something in front of it terminated TLS,
// for no security gained: an attacker who controls the scheme already controls the
// origin.
//
// This uses Host() rather than Hostname() deliberately. Hostname() strips the port, so a
// page at 127.0.0.1:8081 would compare its origin against "http://127.0.0.1" and reject
// every form the browser sent. That is the bug this comment exists to keep from coming
// back.
//
// An absent Origin is refused rather than allowed. Every browser sends it on a POST form
// submission, so a request without one is not a browser filling in this form; it is curl
// or something else, which is exactly what the secret is for.
func (h *Handler) sameOrigin(c fiber.Ctx) bool {
	origin := c.Get("Origin")
	if origin == "" {
		return false
	}

	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}

	return parsed.Host == c.Host()
}
