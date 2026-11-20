package operator

import (
	"time"

	"github.com/google/uuid"
)

// pageData is everything the page template renders.
//
// It is built here rather than read from the repository types so the template never
// reaches into pgtype: a template that had to know whether a text field was Valid
// would be a template doing database work. Everything below is already a value a
// person can read, or empty when there is nothing to show.
type pageData struct {
	// Reports is the page, already rendered to strings.
	Reports []reportView

	// Summary is the context above the list, read separately and never paginated or
	// filtered: how many accounts exist and what is published.
	Summary summaryView

	Page       int
	Limit      int
	Total      int64
	TotalPages int
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string

	// Status is the filter currently applied, empty when the page shows every status.
	Status string

	// Filters is one entry per status plus "all", each carrying the count behind it and
	// whether it is the selected one.
	Filters []filterView

	// Notice is a message shown once after a redirect, and NoticeKind says whether it
	// is a success or a failure.
	Notice     string
	NoticeKind string

	// Now is when the page was rendered, shown in the footer so a stale tab is
	// recognizable as stale.
	Now time.Time
}

// reportView is one report as the template renders it.
//
// The context fields are strings that are either real values or empty, because the
// template's question is "is there something to show", not "is this NULL".
type reportView struct {
	ID      uuid.UUID
	Type    string
	Message string
	Status  string

	Platform   string
	AppVersion string
	AppBuild   string

	DisplayName string
	Email       string

	// CreatedAge is a relative age such as "2 hours ago", which is what a person
	// scanning a queue actually reads. CreatedAt is the exact moment, shown as a title
	// attribute so hovering gives the full value.
	CreatedAge string
	CreatedAt  time.Time
	UpdatedAt  time.Time

	// Context is the joined context line, already assembled: "android · 1.3.1 (7)".
	// Assembled here rather than in the template so the separators live in one place.
	Context string

	// Actions is the status changes offered for this report, in the order they are
	// shown. Delete is always offered and is not in this list, because it is not a
	// status change.
	Actions []actionView

	// HiddenFields are the values every form for this report carries, so acting on a
	// report returns the reader to the page and filter they were on rather than to the
	// top of the list.
	HiddenFields []formField
}

// formField is one hidden input in a form.
type formField struct {
	Name  string
	Value string
}

// actionView is one status button.
//
// The buttons are enumerated here rather than derived in the template, so the lifecycle
// is stated once, in Go, where it can be tested. A template that computed which
// transition was legal would be the second place the lifecycle lives.
type actionView struct {
	Status string
	Label  string

	// Primary marks the forward action, which is the one the page styles as the
	// expected next step. Closing a new report is legitimate but not the default, so it
	// renders as secondary.
	Primary bool
}

// filterView is one filter chip.
type filterView struct {
	Label    string
	Value    string
	Count    int64
	Selected bool
	URL      string
}

// summaryView is the context above the list.
//
// Everything is pre-rendered for the same reason the reports are: the template renders
// values, it does not decide anything. In particular SupportedGap is decided here, because
// the relationship between the two release bounds is the one thing on this strip that needs
// explaining rather than printing.
type summaryView struct {
	Users int64

	// Releases is one entry per published platform. Empty means nothing has been published
	// at all, which the template says in words rather than showing a blank row.
	Releases []releaseView
}

// releaseView is one platform's published policy, as a person reads it.
//
// Latest and MinSupported are two blocks rather than one sentence, because they are two
// facts an operator checks separately: what is being offered, and what is still allowed.
type releaseView struct {
	Platform string

	// Latest is the build currently offered: "1.3.1 (build 7)".
	Latest      string
	PublishedAt time.Time

	// GitTag names the commit, DownloadURL the file, and Size its size. Together they are
	// how an operator confirms that what is published is what they meant to publish.
	GitTag      string
	DownloadURL string
	Size        string

	// MinSupported is the oldest build still accepted. It has no artifact beside it because
	// nothing downloads it: it is a floor, not an offer.
	MinSupported string

	// MinIsLatest says the floor is the newest build, so every earlier one is refused. It is
	// worth naming because it is easy to publish a build and not notice that doing so moved
	// the floor onto itself.
	MinIsLatest bool
}

// noticeSuccess and noticeFailure are the two kinds of inline notice.
const (
	noticeSuccess = "success"
	noticeFailure = "failure"
)
