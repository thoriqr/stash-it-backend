# Discovery — Product Brief

Purpose of this document: give developers and coding agents a concise picture of
what Discovery is and what is being built right now, so that work stays aligned
with the current phase instead of drifting toward the long-term vision.

## What Discovery is

Discovery is a personal place for things a user finds on the internet. They save
an item and can find it and return to it later.

The product exists to shorten the distance between _finding something worth
keeping_ and _actually going back to it_. Everything else supports that path or
is optional.

## The core loop

```
Discover
  ↓
Save
  ↓
Organize (optional)
  ↓
Come Back
  ↓
Take Action
```

Saving must never depend on organizing, and organizing is never a precondition
for keeping something.

## The core object

Everything in Discovery centers on one object: a **Saved Item**, identified by a
URL and enriched with metadata from the original source.

A Saved Item should feel like a useful preview of what the user saved, not just
a stored URL.

Metadata may include information such as:

- title
- description
- domain
- platform
- preview image URL
- canonical URL

Preview images are references to images provided by the original source. Discovery
does not own or guarantee the availability of those external images.

The initial Saved Item is deliberately minimal:

| Field           | Notes                                                      |
| --------------- | ---------------------------------------------------------- |
| `id`            |                                                            |
| `user_id`       | owner, always from authentication, never from the client   |
| `url`           | the thing saved, supplied by the client                    |
| `domain`        | derived from the URL, never fetched from the remote source |
| `platform`      | content/source platform, null until enrichment runs        |
| `title`         | null until enrichment runs                                 |
| `created_at`    |                                                            |
| `updated_at`    |                                                            |
| `collection_id` | owning collection, required in the database                |

### Current Saved Item behavior

- `user_id` comes from authentication, never from the request.
- `url` comes from the client.
- `domain` is derived server-side from the URL: lowercase the hostname and remove
  one leading `www.`. Other subdomains are preserved; no public suffix or
  registrable-domain detection is performed.
- `platform` is the **content/source** platform (youtube, tiktok, instagram,
  pinterest, ...), stays NULL until enrichment, and is never derived from the
  client's platform header.
- `title` stays NULL until enrichment.
- `collection_id` is required and points to the item's current collection.
  Newly saved items go to the user's `Unsorted` collection.
- `enrichment_status` starts at `pending` and is advanced by the later background
  enrichment process, which does not exist yet.

`X-Platform` describes the client/device platform (web/android/ios) and is a
session concern. It must never be used for `saved_items.platform`.

Saving never contacts the remote source: domain derivation is local, and metadata
enrichment happens later without blocking the initial save.

### The Unsorted collection

`Unsorted` is the default collection every user starts with. It lets saving
remain independent of organizing.

- When a registration is finalized and the account becomes permanent, that user
  receives their `Unsorted` collection.
- A registration that is still pending has no account yet and no collections.
- Every newly saved item belongs to the user's `Unsorted` collection.
- `Unsorted` is allowed to stay empty; it is never required to hold anything,
  and it is never removed when its items move elsewhere.

### Collections

`Unsorted` is where saving lands; user collections are where items go when they
are organized. An item may remain in `Unsorted` indefinitely.

- A user names the collection when filing an item into it. If that collection
  does not exist yet it is created as part of the same action, so a collection is
  never created holding nothing.
- A user collection belongs to exactly one user. Only the owner can file an item
  into it, and only their own items can be moved.
- Collection names are unique per user. `"Wishlist"`, `"wishlist"` and
  `" Wishlist "` are the same name, so organizing stays predictable instead of
  producing near-duplicate piles. Surrounding whitespace is not stored.
- Names already reserved by a system collection, such as `Unsorted`, cannot be
  used for a user collection.
- Filing an item that is already in that collection succeeds and changes nothing,
  so repeating the action — or retrying after a dropped connection — is harmless.
- Organizing never waits on or alters metadata enrichment; an item can be moved
  in any enrichment state.

## Current phase: Phase B — basic organization and return

Phase A established authentication, saving a URL, and the Inbox. Phase B makes
saved items findable and organizable again.

Implemented:

- Authentication
- Save a URL
- Inbox
- Saved item detail
- Delete a saved item
- Collections, including filing an item into a collection of the user's own
- Basic search, across saved items and collections

Saved item detail and delete were built during Phase A to complete the core loop.
Collections and Basic Search complete Phase B.

The database foundation is in place: `collections`, `saved_items.collection_id`,
and the saved item enrichment state columns. The enrichment columns exist, but
metadata extraction remains a later phase.

### Search

Search answers one question: where is the thing I saved? One query searches the
user's saved items and collections and returns both result groups together.

**What it searches.** Saved items are matched on their title, their domain, and
their URL. Collections are matched on their name. System collections take part
too, so searching for "unsorted" finds the Unsorted collection. Nothing else is
searchable: not the content platform, not identifiers, not dates. A saved item
result does report which collection it is in, so the user can see where a result
lives, but that collection is not itself searchable.

**Matching.** A query that appears in a result matches it. That is the normal
case, and it covers partial words: "ca" finds "camera". Because people mistype,
a result that does not contain the query exactly can still match when it is
close enough, so "camra" finds "camera" and "sourdogh" finds "sourdough".

A query must be 2–128 characters after trimming. Surrounding whitespace is
ignored, and case is ignored throughout.

Fuzzy matching exists only to recover typos. A single search does both exact and
fuzzy matching, with exact matches above approximate ones. It is strict enough
that unrelated queries return nothing. **No meaningful match is a normal,
successful outcome, not an error.**

**Ordering.** Results come back best match first. Recency matters only to break
a tie between results that matched equally well, so a newer item never outranks a
better match. Ordering is deterministic, so the same search twice gives the same
results in the same order.

The relevance behind that ordering is a ranking detail rather than part of what
the user or the client sees: results are returned already ordered, and no score
is exposed. The matching behavior above is the promise, not any particular number
attached to a result.

**Reach and ownership.** Search only returns the user's own saved items and
collections. Results are capped rather than paged. Paging, autocomplete, and
search suggestions are not part of this version.

**How it works.** Search is native to the existing PostgreSQL database, with no
external search engine or PostgreSQL full-text search in this version.

## Out of scope for the core MVP

These are excluded on purpose. Their absence is a decision, not a gap.

- AI
- Screenshot / media cloud storage
- Price tracking
- Customer web workspace
- Social features

## Later phases

Optional and additive, in rough order of leverage. None of them are prerequisites
for the core loop.

- Background URL metadata extraction
- Reminders
- Richer product metadata
- Comparison
- Optional AI-assisted extraction or comparison
- Optional screenshot / media storage
- Price tracking and alerts
- Customer web
- Internal admin capabilities

## Product principles

- **The value is the short path**: share → save → Inbox → search or collection →
  return later.
- **Saving must be fast.** Metadata extraction must never block the initial save.
  Anything slow happens afterwards, if at all. Deriving the domain locally from
  the URL keeps saving free of network calls entirely.
- **Organization is optional, not mandatory.** An inbox with unsorted items is a
  valid, successful state.
- **AI is an enhancement, not a foundation.** The product must be complete and
  useful with no AI anywhere in it.
- **Comparison is advanced**, and never part of the capture flow.
- **Screenshots and media storage come later**, because storage and bandwidth
  introduce real ongoing cost.
- **No complexity before the core proves useful.** Resist adding features simply
  because they seem to belong in the category.
- **The final vision is not the finish line.** A working product covering share,
  save, collections, search, and metadata is a valid outcome.

## Development order

1. Share → Save → Inbox _(done)_
2. Collections + Search _(done)_
3. Background metadata extraction
4. Reminder / return loop
5. Product metadata enrichment
6. Comparison
7. Optional AI
8. Optional screenshot / media storage
9. Advanced price / decision features
10. Customer web / advanced infrastructure
