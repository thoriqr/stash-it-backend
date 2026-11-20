package operator

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

// sampleResult is a page of reports covering every status and every context shape, so a
// rendered page exercises the template's branches rather than only its happy path.
func sampleResult(now time.Time) ListResult {
	newReport := Report{
		ID:          uuid.MustParse("01a0f359-093b-737a-963a-80f7ca6768ed"),
		Type:        "bug",
		Message:     "Saving an Instagram reel leaves the title empty, so the item shows up in the Inbox without anything to identify it.\n\nIt only happens on the reel link; a normal post works.",
		Status:      StatusNew,
		Platform:    pgtype.Text{String: "android", Valid: true},
		AppVersion:  pgtype.Text{String: "1.3.1", Valid: true},
		AppBuild:    pgtype.Int4{Int32: 7, Valid: true},
		CreatedAt:   pgtype.Timestamptz{Time: now.Add(-2 * time.Hour), Valid: true},
		UpdatedAt:   pgtype.Timestamptz{Time: now.Add(-2 * time.Hour), Valid: true},
		DisplayName: "Thoriq",
		Email:       "thoriq@example.com",
	}

	noContextReport := Report{
		ID:          uuid.MustParse("01a0f359-093b-737a-963a-80f7ca6768ee"),
		Type:        "idea",
		Message:     "Add a way to sort a collection by platform.",
		Status:      StatusNew,
		CreatedAt:   pgtype.Timestamptz{Time: now.Add(-26 * time.Hour), Valid: true},
		UpdatedAt:   pgtype.Timestamptz{Time: now.Add(-26 * time.Hour), Valid: true},
		DisplayName: "Ayu",
		Email:       "ayu@example.com",
	}

	plannedReport := Report{
		ID:          uuid.MustParse("01a0f359-093b-737a-963a-80f7ca6768ef"),
		Type:        "other",
		Message:     "Kenapa link Nintendo nggak bisa dibaca?",
		Status:      StatusPlanned,
		Platform:    pgtype.Text{String: "windows", Valid: true},
		AppVersion:  pgtype.Text{String: "1.3.1", Valid: true},
		AppBuild:    pgtype.Int4{Int32: 7, Valid: true},
		CreatedAt:   pgtype.Timestamptz{Time: now.Add(-72 * time.Hour), Valid: true},
		UpdatedAt:   pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true},
		DisplayName: "Budi",
		Email:       "budi@example.com",
	}

	return ListResult{
		Reports:    []Report{newReport, noContextReport, plannedReport},
		Page:       1,
		Limit:      20,
		Total:      23,
		TotalPages: 2,
		CountsByStatus: map[string]int64{
			StatusNew:     19,
			StatusPlanned: 3,
			StatusClosed:  1,
		},
	}
}

// emptySummary is a deployment with no accounts published into and nothing released.
func emptySummary() Summary {
	return Summary{}
}

// sampleSummary is a deployment with accounts and two published platforms. Android's floor
// is older than its latest build; Windows' floor is its latest build.
func sampleSummary(now time.Time) Summary {
	return Summary{
		Users: 42,
		Releases: []ReleaseSummary{
			{
				Platform:      "android",
				LatestVersion: "1.3.1",
				LatestBuild:   7,
				PublishedAt:   pgtype.Timestamptz{Time: now.Add(-48 * time.Hour), Valid: true},
				GitTag:        "android-v1.3.1-7",
				DownloadURL:   "https://downloads.example.com/android/stash-it-1.3.1-7.apk",
				FileSizeBytes: 48231001,
				MinVersion:    "1.3.0",
				MinBuild:      5,
			},
			{
				Platform:      "windows",
				LatestVersion: "1.3.1",
				LatestBuild:   7,
				PublishedAt:   pgtype.Timestamptz{Time: now.Add(-48 * time.Hour), Valid: true},
				GitTag:        "windows-v1.3.1-7",
				DownloadURL:   "https://downloads.example.com/windows/stash-it-1.3.1-7.msix",
				FileSizeBytes: 91234567,
				MinVersion:    "1.3.1",
				MinBuild:      7,
			},
		},
	}
}

// renderToStrings renders one template the way the handler does.
func renderToString(t *testing.T, data pageData) string {
	t.Helper()

	var buffer bytes.Buffer

	require.NoError(t, render(&buffer, "index.html", data))

	return buffer.String()
}

// TestRenderPage renders a representative page and writes it to testdata so it can be
// opened in a browser.
//
// The assertions are the properties a person would notice missing — the reports, the
// counts, the escaped message, the actions each status offers. Everything visual is
// checked by looking at the file, which is what it is written for.
func TestRenderPage(t *testing.T) {
	now := time.Date(2026, time.November, 21, 5, 30, 0, 0, time.UTC)

	data := newPageData(sampleResult(now), sampleSummary(now), noticeFor("", ""))
	data.Now = now

	rendered := renderToString(t, data)

	require.Contains(t, rendered, "Saving an Instagram reel leaves the title empty")
	require.Contains(t, rendered, "Thoriq")
	require.Contains(t, rendered, "thoriq@example.com")
	require.Contains(t, rendered, "android · 1.3.1 (7)")
	require.Contains(t, rendered, "windows · 1.3.1 (7)")

	// A new report offers both transitions; a planned one offers only Close; the
	// planned fixture is the only one, so Reopen is absent from this page.
	require.Contains(t, rendered, "Accept as planned")
	require.NotContains(t, rendered, "Reopen")

	// The chips carry the counts: 19 + 3 + 1 = 23 for All.
	require.Contains(t, rendered, ">23<")
	require.Contains(t, rendered, ">19<")

	// Two pages, and this is the first, so only Next is a link.
	require.Contains(t, rendered, "Page 1 of 2")
	require.Contains(t, rendered, "Next")

	// Every form carries the page and filter, so acting does not lose the reader's place.
	require.Contains(t, rendered, `name="page" value="1"`)

	out := filepath.Join(t.TempDir(), "operator-preview.html")
	require.NoError(t, os.WriteFile(out, []byte(rendered), 0o600))

	t.Logf("preview written to %s", out)
}

// A message is user input rendered into a page that shows every user's email, so it must
// not be able to inject markup. html/template escapes it; this proves the template does
// not defeat that with template.HTML.
func TestRenderPageEscapesUserInput(t *testing.T) {
	now := time.Now()

	data := newPageData(ListResult{
		Reports: []Report{{
			ID:          uuid.MustParse("01a0f359-093b-737a-963a-80f7ca6768ed"),
			Type:        "bug",
			Message:     `<script>alert("x")</script> & <img src=x onerror=alert(1)>`,
			Status:      StatusNew,
			CreatedAt:   pgtype.Timestamptz{Time: now, Valid: true},
			UpdatedAt:   pgtype.Timestamptz{Time: now, Valid: true},
			DisplayName: "<b>name</b>",
			Email:       "x@example.com",
		}},
		Limit: 20,
	}, emptySummary(), noticeFor("", ""))

	rendered := renderToString(t, data)

	require.NotContains(t, rendered, "<script>alert")
	require.NotContains(t, rendered, "<img src=x")
	require.Contains(t, rendered, "&lt;script&gt;")
	require.Contains(t, rendered, "&lt;b&gt;name&lt;/b&gt;")
}

// An empty queue renders the empty state rather than a blank page, and the pager is
// absent because there is nothing to page through.
func TestRenderPageEmpty(t *testing.T) {
	data := newPageData(ListResult{Limit: 20}, emptySummary(), noticeFor("", ""))

	rendered := renderToString(t, data)

	require.Contains(t, rendered, "Nothing here yet")
	require.NotContains(t, rendered, "Page 1 of")
}

// A filter with no matches says so and names the filter, rather than claiming the whole
// queue is empty.
func TestRenderPageEmptyFiltered(t *testing.T) {
	data := newPageData(
		ListResult{Limit: 20, Status: StatusClosed},
		emptySummary(),
		noticeFor("", ""),
	)

	rendered := renderToString(t, data)

	require.Contains(t, rendered, "No reports with status")
	require.NotContains(t, rendered, "Nothing here yet")
}

// A redirect's message reaches the page after the redirect.
func TestRenderPageNotice(t *testing.T) {
	data := newPageData(
		ListResult{Limit: 20},
		emptySummary(),
		noticeFor("Report deleted.", noticeSuccess),
	)

	rendered := renderToString(t, data)

	require.Contains(t, rendered, "Report deleted.")
	require.Contains(t, rendered, noticeSuccess)
}

// The summary strip reports the account count and, per platform, what is published and what
// is still supported as two separate facts.
//
// They are deliberately not combined into a verdict. The operator reads "latest published"
// and "min supported" separately, and a sentence computed from the pair would answer a
// question they did not ask.
func TestRenderPageSummary(t *testing.T) {
	now := time.Date(2026, time.November, 21, 5, 30, 0, 0, time.UTC)

	data := newPageData(
		ListResult{Limit: 20},
		sampleSummary(now),
		noticeFor("", ""),
	)
	data.Now = now

	rendered := renderToString(t, data)

	require.Contains(t, rendered, "Registered users")
	require.Contains(t, rendered, ">42<")

	// Both platforms get their own card.
	require.Contains(t, rendered, "android")
	require.Contains(t, rendered, "windows")

	// Latest and minimum are each present, for each platform.
	require.Contains(t, rendered, "Latest published")
	require.Contains(t, rendered, "Min supported")
	require.Contains(t, rendered, "1.3.1 (build 7)")
	require.Contains(t, rendered, "1.3.0 (build 5)")

	// The artifact details sit beside the latest build, which is how an operator confirms
	// what they shipped.
	require.Contains(t, rendered, "android-v1.3.1-7")
	require.Contains(t, rendered, "windows-v1.3.1-7")
	require.Contains(t, rendered, "stash-it-1.3.1-7.apk")
	require.Contains(t, rendered, "46.0MB")

	// The publish date is shown, so "the last thing I shipped" answers itself.
	require.Contains(t, rendered, "19 Nov 2026")
}

// Windows has its floor at the newest build, so every earlier one is refused. That is worth
// saying without turning the whole strip into a verdict.
func TestRenderPageSummary_FloorAtLatestIsNamed(t *testing.T) {
	now := time.Date(2026, time.November, 21, 5, 30, 0, 0, time.UTC)

	data := newPageData(
		ListResult{Limit: 20},
		sampleSummary(now),
		noticeFor("", ""),
	)

	rendered := renderToString(t, data)

	require.Contains(t, rendered, "every older build is refused")
}

// The summary is not affected by the list's pagination or filter. This is the property the
// strip exists for: those numbers describe the deployment, not the page on screen.
func TestRenderPageSummaryIgnoresPagingAndFilter(t *testing.T) {
	now := time.Date(2026, time.November, 21, 5, 30, 0, 0, time.UTC)

	page := ListResult{
		Limit:      20,
		Page:       3,
		Total:      61,
		TotalPages: 4,
		Status:     StatusClosed,
	}

	data := newPageData(page, sampleSummary(now), noticeFor("", ""))
	rendered := renderToString(t, data)

	// The listed page is the closed filter, on page 3.
	require.Contains(t, rendered, "Page 3 of 4")

	// The summary still reports the deployment, not the filter.
	require.Contains(t, rendered, "Registered users")
	require.Contains(t, rendered, ">42<")
	require.Contains(t, rendered, "android")
}

// A deployment that has published nothing says so instead of showing a version it invented.
func TestRenderPageSummaryNothingPublished(t *testing.T) {
	data := newPageData(
		ListResult{Limit: 20},
		Summary{Users: 3},
		noticeFor("", ""),
	)

	rendered := renderToString(t, data)

	require.Contains(t, rendered, "Nothing published yet")
	require.Contains(t, rendered, ">3<")
	// No platform card exists, so no bound is printed for one.
	require.NotContains(t, rendered, "Min supported")
}

// The lifecycle is stated once, in Go, so it can be asserted without rendering anything.
func TestActionsFor(t *testing.T) {
	require.Equal(t, []actionView{
		{Status: StatusPlanned, Label: "Accept as planned", Primary: true},
		{Status: StatusClosed, Label: "Close"},
	}, actionsFor(StatusNew))

	require.Equal(t, []actionView{
		{Status: StatusClosed, Label: "Close", Primary: true},
	}, actionsFor(StatusPlanned))

	require.Equal(t, []actionView{
		{Status: StatusNew, Label: "Reopen"},
	}, actionsFor(StatusClosed))
}

// A report whose client sent no context renders no context line rather than a line of
// separators.
func TestContextLineDropsEmptyParts(t *testing.T) {
	require.Empty(t, contextLine("", "", ""))
	require.Equal(t, "android", contextLine("android", "", ""))
	require.Equal(t, "1.3.1", contextLine("", "1.3.1", ""))
	require.Equal(t, "build 7", contextLine("", "", "7"))
	require.Equal(t, "android · 1.3.1 (7)", contextLine("android", "1.3.1", "7"))
	require.Equal(t, "android · build 7", contextLine("android", "", "7"))
}

// The pager keeps the filter, because losing it on "next" would show a different list
// than the one being read. url.Values sorts its keys, so limit comes before page.
func TestPageURLKeepsFilter(t *testing.T) {
	require.Equal(t, "/?limit=20&page=2", pageURL(2, 20, ""))
	require.Equal(t, "/?limit=20&page=2&status=planned", pageURL(2, 20, StatusPlanned))
}
