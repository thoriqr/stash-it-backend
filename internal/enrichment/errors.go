package enrichment

import "errors"

// Errors returned by this package.
//
// They are sentinels rather than apperror values because this package is not an
// API layer: it has no HTTP status code to report and must not depend on the
// transport it happens to be called from. A caller maps these onto whatever its
// own contract needs, which is why they are checkable with errors.Is.
//
// Enrichment is expected to be called from two places with different needs. The
// background worker has to decide whether a job is worth retrying, and the
// synchronous endpoint has to decide what to tell the user. FailureKind exists so
// neither of them has to re-derive that distinction from the error text.
var (
	// ErrInvalidURL is returned when the supplied URL cannot even be parsed, so
	// there is no request to make.
	ErrInvalidURL = errors.New("url cannot be used")

	// ErrFetchFailed is returned when the request itself failed: DNS, the
	// connection, TLS, a timeout, too many redirects, or a refusal by the
	// outbound fetch policy.
	ErrFetchFailed = errors.New("fetching the page failed")

	// ErrParseFailed is returned when the body could not be read as a document.
	// HTML in the wild is rarely well formed, so this is reserved for a failure
	// to read at all rather than for anything the parser found odd.
	ErrParseFailed = errors.New("reading the page failed")

	// ErrUnexpectedStatus is returned when the response arrived but its status
	// was not a success. A 404 or a 500 is a real answer from the origin, so it
	// is a failure rather than a page with no metadata.
	//
	// The sentinel describes the event and not its classification: the same
	// sentinel covers both a terminal 404 and a temporary 503, which are treated
	// differently by FailureKind. The status is in the message, so the log line
	// that reports a failure still says which one it was.
	ErrUnexpectedStatus = errors.New("page returned an unexpected status")

	// ErrUnsupportedContentType is returned when the response is not something
	// worth parsing as HTML, such as an image, a PDF, or an archive.
	ErrUnsupportedContentType = errors.New(
		"response is not an html document",
	)
)

// FailureKind classifies an enrichment failure so that callers can react without
// matching on individual sentinels.
type FailureKind string

const (
	// FailureFetch is a failure to obtain a parseable page at all. Retrying may
	// succeed, because the cause was the network or the origin at that moment:
	// DNS, the connection, TLS, a timeout, too many redirects, a refusal by the
	// outbound fetch policy, a body that could not be read, and the statuses an
	// origin uses to say "not right now" (429 and 5xx).
	FailureFetch FailureKind = "fetch"

	// FailureContent is a page that was reached and cannot be used, such as a
	// non-HTML resource or a terminal client status such as 401, 403, 404 or
	// 410. Retrying will not help.
	//
	// The distinction from FailureFetch is the question a retry asks: whether
	// the origin could plausibly answer differently. A page that was moved, is
	// gone, or is not an HTML document cannot; a page whose origin was
	// overloaded, rate limiting or temporarily broken can.
	FailureContent FailureKind = "content"

	// FailureParse is a page that was reached and is HTML, but could not be
	// understood. Retrying will not change the response.
	FailureParse FailureKind = "parse"
)

// Failure is an enrichment error carrying its classification.
//
// Err is one of the sentinels above, so both errors.Is(err, ErrFetchFailed) and
// errors.As(err, *Failure) work as expected. The two are provided together
// because the caller almost always wants both: the kind to decide whether to
// retry, and the sentinel to describe what went wrong in a log line.
type Failure struct {
	Kind FailureKind
	Err  error
}

func (f *Failure) Error() string {
	return f.Err.Error()
}

func (f *Failure) Unwrap() error {
	return f.Err
}

// newFailure wraps err with its kind, preserving the cause.
//
// The cause is kept rather than replaced, because a failure with no address, no
// status and no reason in it is not diagnosable. Rendering an enrichment error to
// a user is the caller's decision and is made through its own error type, not by
// reading this string.
func newFailure(kind FailureKind, err error) *Failure {
	return &Failure{
		Kind: kind,
		Err:  err,
	}
}

// wrapFailure attaches kind to err unless it already carries a kind, so that the
// innermost classification is the one that survives.
func wrapFailure(kind FailureKind, err error) error {
	if err == nil {
		return nil
	}

	var failure *Failure
	if errors.As(err, &failure) {
		return err
	}

	return newFailure(kind, err)
}

// Kind reports how err should be treated.
//
// A nil error has no kind. An error that did not come from this package is
// reported as FailureFetch, which is the conservative choice: it suggests the
// request may be worth retrying, and losing a recoverable failure costs more than
// retrying a permanent one once.
func Kind(err error) FailureKind {
	if err == nil {
		return ""
	}

	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Kind
	}

	return FailureFetch
}
