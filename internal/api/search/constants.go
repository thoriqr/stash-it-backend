package search

import "time"

// SearchUserRateLimitMax and SearchUserRateLimitWindow bound how often one
// authenticated user may run a search.
//
// Search is the only route in the application whose cost is proportional to
// something the caller owns rather than being a fixed amount of work, and that
// is what makes it need a budget. Both search statements are user-scoped, so the
// plan examines that account's rows and no others — but it examines all of
// them. The predicate is three `ILIKE '%q%'` clauses and three
// `word_similarity()` calls per row, and the function form of `word_similarity`
// cannot be answered from the trigram index, so the index changes the access
// path without reducing the rows read. migrations/000023 already records this:
// domain and url are "simply resolved by a sequential scan on the user-scoped
// subset".
//
// The consequence is that cost grows with the account rather than with the
// request, and nothing else bounds it. This endpoint is not paged and the caller
// cannot widen the result cap, so the *output* is already bounded at
// SavedItemMaxLimit rows; the *scan* is not. Search is also the only route here
// that can be repeated for free: unlike a save or a collection deletion, running
// it again costs the caller nothing, so an account that has accumulated rows can
// keep re-triggering the full scan indefinitely without paying for it first.
//
// The subject is the authenticated user rather than a client address, matching
// the two saved-item budgets. The rows being scanned belong to the account, the
// caller chooses its own network path, an address budget would not survive IP
// rotation, and it would charge a household or an office behind one NAT for one
// person's searching.
//
// Thirty a minute sits comfortably above interactive use. A search box is driven
// by a person adjusting a query, so even deliberate exploration is a handful of
// requests a minute and ordinary use is far below that; thirty leaves room for a
// client that re-queries as the user types without ever being the constraint. It
// is deliberately three times the enrichment budget, because the two routes have
// opposite interactive characters: refreshing an item's metadata is something a
// user does when a page looks wrong, while searching is something they do
// repeatedly and on purpose.
//
// The one-minute window matches that interactive character. A search box that
// stops working for ten minutes after a burst is a far worse experience than one
// that recovers in a minute, and the requests being bounded are short enough —
// milliseconds for a small account — that a minute is long enough to average out
// a burst rather than cut one in half.
//
// **This is a provisional threshold, not a benchmark-derived or
// production-validated one.** The cost profile behind it was measured against a
// disposable PostgreSQL container running this project's baseline schema, which
// established that search cost grows with the account's row count and that a
// query matching nothing costs the same as one that matches something. It did
// not establish an optimal rate: those numbers came from an idle developer
// machine, not from production hardware under concurrent load, and absolute
// latency will differ. The value here is chosen to be safe on the *shape* of the
// problem — a user-keyed ceiling well clear of interactive use — not to sit at a
// measured optimum. Revisit against real traffic.
const (
	SearchUserRateLimitMax    = 30
	SearchUserRateLimitWindow = time.Minute
)

// searchUserRateLimitNamespace groups the search counters in Redis.
//
// It is its own namespace, separate from both saved-item budgets. They cost
// different things and one flow's flood must not spend another's allowance: a
// save spends a database insert, a queue write and an eventual worker fetch,
// enrichment spends an outbound fetch in the API process, and a search spends a
// user-scoped scan whose cost depends on how many rows that account has. A user
// who has saturated one must still be able to use the others.
//
// It is separate from every auth namespace because reaching this route is not an
// authentication attempt. An exhausted search budget must never lock anyone out
// of signing in, and must never be a way to make one flow's flood land on
// another's allowance.
const searchUserRateLimitNamespace = "search-user"

// The remaining constants describe what one request may ask for. They are the
// bounds on the *result*, which is a different thing from the bounds on how
// often the route may be reached; see SearchUserRateLimitMax above for why only
// the second of those needed a budget.

const (
	// QueryMinLength is the shortest query that can produce a meaningful match,
	// measured in runes after trimming. A single rune matches far too much to be
	// useful and forces the fuzzy threshold to admit noise.
	QueryMinLength = 2

	// QueryMaxLength bounds a search query, measured in runes after trimming. No
	// realistic query approaches it, and an unbounded query is an unbounded
	// amount of trigram work in the database.
	QueryMaxLength = 128

	// SavedItemDefaultLimit is how many saved items a search returns when the
	// caller does not ask for a different amount.
	SavedItemDefaultLimit = 20

	// SavedItemMaxLimit is the most saved items a single search can return. The
	// limit exists because search is not paged, so it is the only thing bounding
	// the size of the work one request can cause.
	SavedItemMaxLimit = 50

	// CollectionLimit is how many collections a search returns. It is fixed rather
	// than caller controlled because collections are a navigation aid shown
	// alongside the results, not a list to page through.
	CollectionLimit = 5
)
