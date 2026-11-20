package integration_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/operator"
	feedbackdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/feedback/generated"
)

// The operator page is driven here through its own Fiber app, exactly as Start mounts it,
// without binding a port. That is the whole surface: the guard, the SQL, the template and
// the redirects.
//
// These tests are what lets the page be trusted without a browser: they prove it reads and
// writes the same rows the API writes, that a write is refused unless it came from the
// page itself, and that a status change and a delete land where they should.

// operatorOrigin is the origin a browser on the page's own host sends.
const operatorOrigin = "http://example.com"

// operatorApp builds the page over the suite's pool, with the tables it reads cleared.
//
// They are shared with the other integration tests and with each other, so a page that
// counts rows or reads a release channel would otherwise see whatever the previous test left
// behind. Clearing them here rather than in each test keeps that from being something a new
// test forgets. Nothing runs in parallel, so this cannot pull rows out from under a
// concurrent test.
func operatorApp(t *testing.T, secret string) *fiber.App {
	t.Helper()

	_, err := testPool.Exec(
		context.Background(),
		"TRUNCATE TABLE feedback, client_release_channels, client_releases",
	)
	require.NoError(t, err)

	return operator.NewApp(testPool, secret, zap.NewNop())
}

// operatorRequest drives one request against the page.
func operatorRequest(
	t *testing.T,
	app *fiber.App,
	method string,
	target string,
	form url.Values,
	headers map[string]string,
) (int, string, http.Header) {
	t.Helper()

	var body io.Reader

	if form != nil {
		body = strings.NewReader(form.Encode())
	}

	req := httptest.NewRequest(method, target, body)

	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	for name, value := range headers {
		// Host is not an ordinary header in Go: net/http takes it from Request.Host and
		// ignores a Header entry of that name, so setting it the usual way would leave the
		// handler seeing the default host and make a test about ports prove nothing.
		if strings.EqualFold(name, "Host") {
			req.Host = value
			continue
		}

		req.Header.Set(name, value)
	}

	resp, err := app.Test(req)
	require.NoError(t, err)

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(raw), resp.Header
}

// getPage reads the list.
func getPage(t *testing.T, app *fiber.App, query string) (int, string) {
	t.Helper()

	target := "/"
	if query != "" {
		target += "?" + query
	}

	status, body, _ := operatorRequest(t, app, http.MethodGet, target, nil, nil)

	return status, body
}

// postForm submits a form the way the page's own browser would.
func postForm(
	t *testing.T,
	app *fiber.App,
	target string,
	form url.Values,
) (int, http.Header) {
	t.Helper()

	status, _, headers := operatorRequest(
		t,
		app,
		http.MethodPost,
		target,
		form,
		map[string]string{"Origin": operatorOrigin},
	)

	return status, headers
}

// seedReport files one report directly, which is the state the page reads.
func seedReport(
	t *testing.T,
	userID uuid.UUID,
	reportType string,
	message string,
	status string,
) uuid.UUID {
	t.Helper()

	var id uuid.UUID

	err := testPool.QueryRow(
		context.Background(),
		`INSERT INTO feedback (
			user_id, type, message, status, platform, app_version, app_build
		 ) VALUES ($1, $2, $3, $4, 'android', '1.3.1', 7)
		 RETURNING id`,
		userID,
		reportType,
		message,
		status,
	).Scan(&id)
	require.NoError(t, err)

	return id
}

func feedbackStatusOf(t *testing.T, feedbackID uuid.UUID) string {
	t.Helper()

	row, err := feedbackdbtest.New(testPool).GetFeedbackRowForTest(
		context.Background(),
		feedbackID,
	)
	require.NoError(t, err)

	return row.Status
}

func feedbackCountFor(t *testing.T, userID uuid.UUID) int64 {
	t.Helper()

	count, err := feedbackdbtest.New(testPool).CountFeedbackForUser(
		context.Background(),
		userID,
	)
	require.NoError(t, err)

	return count
}

// TestOperatorPage_ListsReportsFiledThroughTheAPI proves the page reads the same rows the
// API writes, with the owner's identity, the client context and the status.
func TestOperatorPage_ListsReportsFiledThroughTheAPI(t *testing.T) {
	email := uniqueEmail("operator-list")
	ownerID := seedFeedbackOwner(t, email)
	app := operatorApp(t, "")

	seedReport(t, ownerID, "bug", "The title is empty for Instagram reels.", "new")
	seedReport(t, ownerID, "idea", "Sort a collection by platform.", "planned")

	status, body := getPage(t, app, "")
	require.Equal(t, http.StatusOK, status)

	require.Contains(t, body, "The title is empty for Instagram reels.")
	require.Contains(t, body, "Sort a collection by platform.")
	require.Contains(t, body, "Feedback Owner")
	require.Contains(t, body, email)
	require.Contains(t, body, "android · 1.3.1 (7)")
}

// The filter narrows the page while the counts still describe the whole queue.
func TestOperatorPage_FiltersByStatus(t *testing.T) {
	ownerID := seedFeedbackOwner(t, uniqueEmail("operator-filter"))
	app := operatorApp(t, "")

	seedReport(t, ownerID, "bug", "A new report.", "new")
	seedReport(t, ownerID, "idea", "A planned report.", "planned")
	seedReport(t, ownerID, "other", "A closed report.", "closed")

	_, body := getPage(t, app, "status=planned")
	require.Contains(t, body, "A planned report.")
	require.NotContains(t, body, "A new report.")
	require.NotContains(t, body, "A closed report.")

	// The chips still count everything, so the other statuses stay visible.
	require.Contains(t, body, ">3<")
}

// A status change from the page writes the column the API would have written.
func TestOperatorPage_SetsStatus(t *testing.T) {
	ownerID := seedFeedbackOwner(t, uniqueEmail("operator-status"))
	app := operatorApp(t, "")

	feedbackID := seedReport(t, ownerID, "bug", "Needs triage.", "new")

	status, headers := postForm(
		t,
		app,
		"/feedback/"+feedbackID.String()+"/status",
		url.Values{
			"status":        {"planned"},
			"page":          {"1"},
			"status_filter": {""},
		},
	)
	require.Equal(t, http.StatusSeeOther, status)
	require.Contains(t, headers.Get("Location"), "notice=status")

	require.Equal(t, "planned", feedbackStatusOf(t, feedbackID))
}

// A delete from the page removes the row and the redirect says so.
func TestOperatorPage_DeletesReport(t *testing.T) {
	ownerID := seedFeedbackOwner(t, uniqueEmail("operator-delete"))
	app := operatorApp(t, "")

	feedbackID := seedReport(t, ownerID, "other", "spam spam spam", "new")

	status, headers := postForm(
		t,
		app,
		"/feedback/"+feedbackID.String()+"/delete",
		url.Values{"page": {"1"}, "status_filter": {""}},
	)
	require.Equal(t, http.StatusSeeOther, status)
	require.Contains(t, headers.Get("Location"), "notice=deleted")

	require.Zero(t, feedbackCountFor(t, ownerID))
}

// Deleting something already gone is reported rather than silently succeeding, so the
// operator learns their page was stale.
func TestOperatorPage_DeleteMissingReport(t *testing.T) {
	app := operatorApp(t, "")

	status, headers := postForm(
		t,
		app,
		"/feedback/"+uuid.NewString()+"/delete",
		url.Values{},
	)
	require.Equal(t, http.StatusSeeOther, status)
	require.Contains(t, headers.Get("Location"), "notice=notfound")
}

// A status the schema does not allow is refused with a notice, and the row keeps what it
// had. The schema's CHECK would reject it too; refusing here is what produces a message
// the page can show.
func TestOperatorPage_RejectsUnknownStatus(t *testing.T) {
	ownerID := seedFeedbackOwner(t, uniqueEmail("operator-bad-status"))
	app := operatorApp(t, "")

	feedbackID := seedReport(t, ownerID, "bug", "Unchanged.", "new")

	status, headers := postForm(
		t,
		app,
		"/feedback/"+feedbackID.String()+"/status",
		url.Values{"status": {"shipped"}},
	)
	require.Equal(t, http.StatusSeeOther, status)
	require.Contains(t, headers.Get("Location"), "notice=invalid")

	require.Equal(t, "new", feedbackStatusOf(t, feedbackID))
}

// A write without the page's own origin is refused, which is what stops a website the
// operator visits from deleting reports through their browser.
//
// Origin is a forbidden header name for JavaScript, so a hostile page cannot forge it: it
// can send the request, but it cannot claim to be this page while doing so.
func TestOperatorPage_RefusesCrossOriginWrite(t *testing.T) {
	ownerID := seedFeedbackOwner(t, uniqueEmail("operator-csrf"))
	app := operatorApp(t, "")

	feedbackID := seedReport(t, ownerID, "bug", "Survives a hostile page.", "new")

	status, _, _ := operatorRequest(
		t,
		app,
		http.MethodPost,
		"/feedback/"+feedbackID.String()+"/delete",
		url.Values{},
		map[string]string{"Origin": "https://evil.example"},
	)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, int64(1), feedbackCountFor(t, ownerID))

	// An absent Origin is refused too: every browser sends one for a form post, so its
	// absence means this is not the page.
	status, _, _ = operatorRequest(
		t,
		app,
		http.MethodPost,
		"/feedback/"+feedbackID.String()+"/delete",
		url.Values{},
		nil,
	)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, int64(1), feedbackCountFor(t, ownerID))
}

// Reading is not blocked by the origin check: only writes are.
func TestOperatorPage_AllowsCrossOriginRead(t *testing.T) {
	app := operatorApp(t, "")

	status, _, _ := operatorRequest(t, app, http.MethodGet, "/", nil, nil)
	require.Equal(t, http.StatusOK, status)
}

// When a secret is configured, every request needs it.
func TestOperatorPage_RequiresSecretWhenConfigured(t *testing.T) {
	app := operatorApp(t, "operator-test-secret")

	status, _, _ := operatorRequest(t, app, http.MethodGet, "/", nil, nil)
	require.Equal(t, http.StatusUnauthorized, status)

	status, _, _ = operatorRequest(
		t,
		app,
		http.MethodGet,
		"/",
		nil,
		map[string]string{operator.SecretHeader: "wrong"},
	)
	require.Equal(t, http.StatusUnauthorized, status)

	status, _, _ = operatorRequest(
		t,
		app,
		http.MethodGet,
		"/",
		nil,
		map[string]string{operator.SecretHeader: "operator-test-secret"},
	)
	require.Equal(t, http.StatusOK, status)
}

// A message containing markup is escaped in the real page, not only in the template test.
func TestOperatorPage_EscapesMessage(t *testing.T) {
	ownerID := seedFeedbackOwner(t, uniqueEmail("operator-escape"))
	app := operatorApp(t, "")

	seedReport(t, ownerID, "bug", `<script>alert("x")</script>`, "new")

	_, body := getPage(t, app, "")

	require.NotContains(t, body, `<script>alert("x")</script>`)
	require.Contains(t, body, "&lt;script&gt;")
}

// Paging moves through the list and reports where it is.
func TestOperatorPage_Pages(t *testing.T) {
	ownerID := seedFeedbackOwner(t, uniqueEmail("operator-pages"))
	app := operatorApp(t, "")

	for range 3 {
		seedReport(t, ownerID, "bug", "Report "+uuid.NewString(), "new")
	}

	status, body := getPage(t, app, "limit=2")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, "Page 1 of 2")
}

// A write from the page's own origin on a listening port is accepted.
//
// This is the case a browser actually produces, and the one the original implementation
// got wrong: Fiber's Hostname() drops the port, so a page at 127.0.0.1:8081 compared the
// Origin against "http://127.0.0.1" and refused every form. The check compares the full
// authority instead, which is what this pins.
func TestOperatorPage_AcceptsOriginWithPort(t *testing.T) {
	ownerID := seedFeedbackOwner(t, uniqueEmail("operator-origin-port"))
	app := operatorApp(t, "")

	feedbackID := seedReport(t, ownerID, "bug", "Filed from a real browser.", "new")

	form := url.Values{"status": {"planned"}}

	status, _, _ := operatorRequest(
		t,
		app,
		http.MethodPost,
		"/feedback/"+feedbackID.String()+"/status",
		form,
		map[string]string{
			"Origin": "http://127.0.0.1:8081",
			"Host":   "127.0.0.1:8081",
		},
	)
	require.Equal(t, http.StatusSeeOther, status)
	require.Equal(t, "planned", feedbackStatusOf(t, feedbackID))
}

// An origin whose port differs from the one serving the page is refused, which is what
// makes the check about this page rather than about "any localhost".
func TestOperatorPage_RefusesOriginWithDifferentPort(t *testing.T) {
	ownerID := seedFeedbackOwner(t, uniqueEmail("operator-origin-other-port"))
	app := operatorApp(t, "")

	feedbackID := seedReport(t, ownerID, "bug", "Different port.", "new")

	status, _, _ := operatorRequest(
		t,
		app,
		http.MethodPost,
		"/feedback/"+feedbackID.String()+"/delete",
		url.Values{},
		map[string]string{
			"Origin": "http://127.0.0.1:9999",
			"Host":   "127.0.0.1:8081",
		},
	)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, int64(1), feedbackCountFor(t, ownerID))
}

// The browser's favicon request is answered rather than logged as a failure.
func TestOperatorPage_ServesFavicon(t *testing.T) {
	app := operatorApp(t, "")

	status, _, headers := operatorRequest(
		t,
		app,
		http.MethodGet,
		"/favicon.ico",
		nil,
		nil,
	)

	require.Equal(t, http.StatusOK, status)
	require.Contains(t, headers.Get("Content-Type"), "icon")
}

// The page's summary reports the account count and, per platform, the published build and
// the minimum still accepted — two facts, shown separately.
//
// Neither moves when the list is paged or filtered: they describe the deployment, not the
// reports on screen.
func TestOperatorPage_SummaryIsIndependentOfTheList(t *testing.T) {
	ownerID := seedFeedbackOwner(t, uniqueEmail("operator-summary"))
	app := operatorApp(t, "")

	seedReport(t, ownerID, "bug", "One.", "new")
	seedReport(t, ownerID, "idea", "Two.", "closed")

	// Android's floor stays behind its latest build; Windows' floor is its latest build.
	androidMinimum := createClientRelease(t, "android", "1.3.0", 5, "minimum")
	androidLatest := createClientRelease(t, "android", "1.3.1", 7, "latest")
	setClientReleaseChannel(t, "android", androidLatest, androidMinimum)

	windowsLatest := createClientRelease(t, "windows", "1.3.1", 7, "latest")
	setClientReleaseChannel(t, "windows", windowsLatest, windowsLatest)

	_, body := getPage(t, app, "")

	require.Contains(t, body, "Registered users")
	require.Contains(t, body, "android")
	require.Contains(t, body, "windows")

	require.Contains(t, body, "Latest published")
	require.Contains(t, body, "Min supported")
	require.Contains(t, body, "1.3.1 (build 7)")
	require.Contains(t, body, "1.3.0 (build 5)")

	// The artifact beside the latest build is how the operator confirms what shipped.
	require.Contains(t, body, "client-v1.3.1-7")

	// Windows' floor is its newest build, which is worth naming.
	require.Contains(t, body, "every older build is refused")

	// The same numbers appear on a filtered, paged view.
	_, filtered := getPage(t, app, "status=closed&limit=1")

	require.Contains(t, body, "Registered users")
	require.Contains(t, filtered, "1.3.1 (build 7)")
	require.Contains(t, filtered, "Min supported")
}

// A deployment that has published nothing says so, rather than showing a version it
// invented. A platform without a channel has no card at all.
func TestOperatorPage_SummaryWithNothingPublished(t *testing.T) {
	app := operatorApp(t, "")

	_, body := getPage(t, app, "")

	require.Contains(t, body, "Nothing published yet")
	require.NotContains(t, body, "Min supported")
}

// Publishing a new build moves what the page reports as latest, and the floor it reports is
// whatever the channel says — which is the point of showing both instead of one verdict.
func TestOperatorPage_SummaryAfterPublishing(t *testing.T) {
	app := operatorApp(t, "")

	first := createClientRelease(t, "android", "1.3.0", 1, "first")
	setClientReleaseChannel(t, "android", first, first)

	_, before := getPage(t, app, "")
	require.Contains(t, before, "1.3.0 (build 1)")
	require.Contains(t, before, "client-v1.3.0-1")

	second := createClientRelease(t, "android", "1.3.1", 2, "second")

	// The minimum deliberately stays on build 1: a normal publish moves only latest.
	_, err := testPool.Exec(
		context.Background(),
		`UPDATE client_release_channels
		 SET latest_release_id = $1
		 WHERE platform = 'android'`,
		second,
	)
	require.NoError(t, err)

	_, after := getPage(t, app, "")

	// Latest moved.
	require.Contains(t, after, "1.3.1 (build 2)")
	require.Contains(t, after, "client-v1.3.1-2")

	// The floor did not, and is still reported.
	require.Contains(t, after, "1.3.0 (build 1)")
	require.NotContains(t, after, "every older build is refused")
}

// The page is not served by the public API: those paths exist only on its own app.
func TestOperatorPage_IsNotOnThePublicAPI(t *testing.T) {
	for _, target := range []string{"/", "/feedback"} {
		status, _ := doUserRequest(t, http.MethodGet, target, nil, "")
		require.NotEqual(t, http.StatusOK, status, target)
	}
}
