package feedback

import "time"

// The kinds a report may have, and the statuses an operator may set, are stated in
// exactly two places and neither is here.
//
// The request validator holds the vocabulary a client may choose from, as the
// `oneof=bug idea other` tag on CreateFeedbackRequest, because that is the thing that
// rejects an unknown value and the thing a client reads. The schema holds the
// vocabulary a row may carry, as feedback_type_check and feedback_status_check.
//
// Constants here would be a third copy with no reader, and the three would drift the
// first time a value is added. The operator page sets a status and this package never
// reads one, so a list here would be documentation pretending to be code.

// MessageMaxLength bounds a report's message, measured in runes.
//
// The bound exists to keep one row from carrying an unbounded document rather than
// to restrict ordinary use, and it is measured in characters rather than bytes so a
// multilingual report is not rejected for being long when it is not. It matches the
// saved item note bound, which is the other free-text field a person writes.
//
// The validator's max=5000 tag on the request cannot reference this value, so the two
// numbers are kept in step by hand. The service applies this one again, which is what
// makes a request that bypassed the handler still bounded.
const MessageMaxLength = 5000

// Bounds on the context a client reports about itself.
//
// These are lengths only, never allowed-value lists. A client running a build this
// deployment has never seen, on a platform added after this code shipped, still has
// something worth recording, and refusing the report over the context describing it
// would lose the report rather than the context.
const (
	PlatformMaxBytes   = 32
	AppVersionMaxBytes = 32
)

// CreateUserRateLimitMax and CreateUserRateLimitWindow bound how many reports one
// authenticated user may file.
//
// This is deliberately the tightest user budget in the application. The work a
// report schedules is only an insert, so what it protects is not a shared resource
// but the operator's attention: a queue that one account can fill at will stops
// being a queue of reports. Five in a day is far above what a person filing a bug
// and an idea needs, and far below what makes the list unreadable.
//
// Note that this does not make the list free of noise — a determined user can still
// file five useless reports a day. Removing what does arrive is the operator's job,
// and the budget exists to bound the flood rather than to filter it.
//
// The window is measured from the first report rather than from midnight, because
// the limiter's window is not aligned to any clock. That is the intended behavior:
// "five per day" is a rate, not a calendar quota.
//
// **This is an initial engineering estimate, not a production-validated
// threshold.** No traffic has been observed. Revisit against real use.
const (
	CreateUserRateLimitMax    = 5
	CreateUserRateLimitWindow = 24 * time.Hour
)

// CreateUserRateLimitNamespace groups the report counters in Redis.
//
// It is its own namespace rather than part of any other budget because one flow's
// flood must not spend another's allowance: a user who has been throttled on filing
// reports must still be able to save items and sign in.
//
// The name says "user" because the subject is the authenticated account, and "create"
// because filing a report is the only thing this feature does. The operator page is
// not rate limited and has no namespace: it reads and writes over a loopback listener
// that must keep working while the rest of the application is being diagnosed, which
// is the same reasoning the session cleanup route states.
//
// It is exported so an integration test can name this budget specifically. A test that
// mounted a limiter refusing everything would prove the route is limited, but not that
// it is limited by *this* budget rather than by whichever one answers first.
const CreateUserRateLimitNamespace = "feedback-create-user"
