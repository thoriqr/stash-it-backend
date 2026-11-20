package operator

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// newPageData builds the template's data from one page of reports.
//
// Everything a template would otherwise compute is computed here: relative ages,
// whether each action applies, which filters are selected, and the URLs for the
// previous and next pages. The template's job is to render, not to decide.
func newPageData(
	result ListResult,
	summary Summary,
	notice noticeMessage,
) pageData {
	reports := make([]reportView, 0, len(result.Reports))

	for _, report := range result.Reports {
		reports = append(reports, newReportView(report, result))
	}

	data := pageData{
		Reports:    reports,
		Summary:    newSummaryView(summary),
		Page:       result.Page,
		Limit:      result.Limit,
		Total:      result.Total,
		TotalPages: result.TotalPages,
		Status:     result.Status,
		Filters:    newFilterViews(result),
		Notice:     notice.Message,
		NoticeKind: notice.Kind,
		Now:        Now(),
	}

	data.HasPrev = data.Page > 1
	data.HasNext = data.Page < data.TotalPages

	if data.HasPrev {
		data.PrevURL = pageURL(data.Page-1, data.Limit, data.Status)
	}

	if data.HasNext {
		data.NextURL = pageURL(data.Page+1, data.Limit, data.Status)
	}

	return data
}

// newReportView renders one report.
//
// result is passed for the page and filter only: every form on the report carries them
// as hidden fields, so a status change or a delete returns the reader to where they
// were rather than to the top of the list.
func newReportView(report Report, result ListResult) reportView {
	view := reportView{
		ID:          report.ID,
		Type:        report.Type,
		Message:     report.Message,
		Status:      report.Status,
		Platform:    report.Platform.String,
		AppVersion:  report.AppVersion.String,
		DisplayName: report.DisplayName,
		Email:       report.Email,
		CreatedAge:  relativeAge(report.CreatedAt.Time),
		CreatedAt:   report.CreatedAt.Time,
		UpdatedAt:   report.UpdatedAt.Time,
		Actions:     actionsFor(report.Status),
		HiddenFields: []formField{
			{Name: "page", Value: strconv.Itoa(result.Page)},
			{Name: "status_filter", Value: result.Status},
		},
	}

	if report.AppBuild.Valid {
		view.AppBuild = strconv.FormatInt(int64(report.AppBuild.Int32), 10)
	}

	view.Context = contextLine(view.Platform, view.AppVersion, view.AppBuild)

	return view
}

// actionsFor lists the status changes a report in this state offers.
//
// The lifecycle is stated once here: a new report can be accepted or closed directly,
// a planned one can only be closed, and a closed one can be reopened because a report
// closed by mistake should not need a database client to undo. Nothing else moves
// backwards — a status that could go both ways would make these buttons a menu rather
// than a decision.
func actionsFor(status string) []actionView {
	switch status {
	case StatusNew:
		return []actionView{
			{Status: StatusPlanned, Label: "Accept as planned", Primary: true},
			{Status: StatusClosed, Label: "Close"},
		}
	case StatusPlanned:
		return []actionView{
			{Status: StatusClosed, Label: "Close", Primary: true},
		}
	default:
		return []actionView{
			{Status: StatusNew, Label: "Reopen"},
		}
	}
}

// newFilterViews builds the chip row, including the count behind each status.
//
// "All" comes first because that is the queue the operator opens: everything not yet
// finished. The three statuses follow in lifecycle order rather than alphabetically.
func newFilterViews(result ListResult) []filterView {
	all := int64(0)
	for _, count := range result.CountsByStatus {
		all += count
	}

	filters := make([]filterView, 0, len(Statuses)+1)

	filters = append(filters, filterView{
		Label:    "All",
		Value:    "",
		Count:    all,
		Selected: result.Status == "",
		URL:      pageURL(1, result.Limit, ""),
	})

	for _, status := range Statuses {
		filters = append(filters, filterView{
			Label:    statusLabel(status),
			Value:    status,
			Count:    result.CountsByStatus[status],
			Selected: result.Status == status,
			URL:      pageURL(1, result.Limit, status),
		})
	}

	return filters
}

// statusLabel is how a status is written for a person.
func statusLabel(status string) string {
	switch status {
	case StatusNew:
		return "New"
	case StatusPlanned:
		return "Planned"
	case StatusClosed:
		return "Closed"
	default:
		return status
	}
}

// contextLine joins the client context into one readable line.
//
// Empty parts are dropped rather than shown as blanks, so a report from a build that
// sent nothing renders no context at all instead of a line of separators.
func contextLine(platform, version, build string) string {
	line := ""

	if platform != "" {
		line = platform
	}

	v := version
	if build != "" {
		if v != "" {
			v = v + " (" + build + ")"
		} else {
			v = "build " + build
		}
	}

	if v != "" {
		if line != "" {
			line = line + " · " + v
		} else {
			line = v
		}
	}

	return line
}

// pageURL builds a link that preserves the current filter.
//
// The filter travels with the page number because losing it on "next" would show the
// operator a different list than the one they were reading.
func pageURL(page, limit int, status string) string {
	query := url.Values{}

	query.Set("page", strconv.Itoa(page))
	query.Set("limit", strconv.Itoa(limit))

	if status != "" {
		query.Set("status", status)
	}

	return "/?" + query.Encode()
}

// newSummaryView renders the context above the list.
//
// Nothing is derived from comparing the two bounds. They are printed as two facts, which is
// what an operator reads them as: the build being offered, and the build below which nothing
// is accepted.
func newSummaryView(summary Summary) summaryView {
	releases := make([]releaseView, 0, len(summary.Releases))

	for _, release := range summary.Releases {
		releases = append(releases, releaseView{
			Platform:     release.Platform,
			Latest:       buildLabel(release.LatestVersion, release.LatestBuild),
			PublishedAt:  release.PublishedAt.Time,
			GitTag:       release.GitTag,
			DownloadURL:  release.DownloadURL,
			Size:         humanSize(release.FileSizeBytes),
			MinSupported: buildLabel(release.MinVersion, release.MinBuild),
			MinIsLatest:  release.MinBuild == release.LatestBuild,
		})
	}

	return summaryView{
		Users:    summary.Users,
		Releases: releases,
	}
}

// buildLabel renders one release as a person reads it.
func buildLabel(version string, build int32) string {
	return version + " (build " + strconv.FormatInt(int64(build), 10) + ")"
}

// humanSize renders a byte count the way a person reads a download size.
//
// It is only ever shown for a file whose size the database already holds, so this is
// formatting rather than measurement. Binary units, because that is what an artifact's size
// means to the person downloading it.
func humanSize(bytes int64) string {
	const unit = 1024

	if bytes < unit {
		return strconv.FormatInt(bytes, 10) + " B"
	}

	div, exp := int64(unit), 0

	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	return strconv.FormatFloat(
		float64(bytes)/float64(div),
		'f',
		1,
		64,
	) + string("KMGT"[exp]) + "B"
}

// relativeAge renders how long ago something happened.
//
// The exact timestamps are still in the page as title attributes, so this is a
// scanning aid rather than the record: "2 hours ago" is what a person reads when
// deciding whether a report is recent, and the title gives the moment when they need
// it.
func relativeAge(moment time.Time) string {
	if moment.IsZero() {
		return ""
	}

	elapsed := time.Since(moment)

	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		minutes := int(elapsed.Minutes())
		return plural(minutes, "minute") + " ago"
	case elapsed < 24*time.Hour:
		hours := int(elapsed.Hours())
		return plural(hours, "hour") + " ago"
	case elapsed < 30*24*time.Hour:
		days := int(elapsed.Hours() / 24)
		return plural(days, "day") + " ago"
	default:
		return moment.Format("2 Jan 2006")
	}
}

func plural(count int, unit string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", unit)
	}

	return fmt.Sprintf("%d %ss", count, unit)
}

// noticeMessage is an inline message shown once after a redirect.
type noticeMessage struct {
	Message string
	Kind    string
}

// noticeFor builds the message a redirect carries.
//
// The message travels in the query rather than in a session, because this page has no
// session and one operator reading their own actions needs no more than that.
func noticeFor(message, kind string) noticeMessage {
	return noticeMessage{Message: message, Kind: kind}
}
