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
| `description`   | null until enrichment runs, and may stay null afterwards   |
| `image_url`     | null until enrichment runs, and may stay null afterwards   |
| `created_at`    |                                                            |
| `updated_at`    |                                                            |
| `collection_id` | owning collection, required in the database                |

Enrichment metadata is additive. A Saved Item is a valid, useful saved item
before any of it arrives, and it stays one when none of it does.

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
- `enrichment_status` starts at `pending` and is advanced by enrichment. It has
  exactly three values: `pending`, `completed`, and `failed`. Enrichment can be
  requested explicitly for one item, and it also happens automatically after a
  save, in the background. There is no `processing` or `retrying` state: whether
  the background process will try again is not a property of the Saved Item, so
  it is never recorded on one.

`X-Platform` describes the client/device platform (web/android/ios) and is a
session concern. It must never be used for `saved_items.platform`.

Saving never contacts the remote source: domain derivation is local, and metadata
enrichment happens later without blocking the initial save.

### Metadata enrichment

Enrichment adds what the original source can tell us about a Saved Item, so the
item reads as a preview of what the user saved rather than a bare link. It is an
enhancement to the core loop, never a condition of it.

- **It is best effort.** Enrichment never blocks the initial save. It can happen
  when a user asks for it, or later through background processing. A user who
  never comes back is no worse off than before it existed.
- **Saving schedules enrichment automatically.** A save records the Saved Item
  first, then queues it for background enrichment, so the user gets their Saved
  Item back immediately and the metadata arrives afterwards. If that background
  step cannot be scheduled, the save still succeeds and the item simply waits in
  `pending` until someone asks for it. Enrichment is never a condition of saving,
  and a Saved Item is never rejected because its page could not be fetched.
- **Background enrichment is limited, not endless.** An unreachable page or a
  temporarily broken origin is retried a bounded number of times with growing
  delays. A page that has been deleted, or that is not an HTML document, is not
  retried at all, because asking again cannot change the answer. Either way the
  item ends up `completed` or `failed`, and the same thing a user-triggered
  enrichment would have produced.
- **A user can ask for one item to be enriched.** Enrichment is available on
  demand for an individual Saved Item, and only for Saved Items the user owns.
  There is no bulk or whole-inbox version: asking for one item does one item's
  worth of work, because enrichment reaches out to the source and that should not
  be something a single action does to many things at once.
- **Asking again is normal.** Enrichment is not restricted to items that have
  never been enriched, or to items whose last attempt failed. Asking again
  refreshes whatever the source says now, and an item whose source has changed
  since it was saved is exactly the case worth asking about again.
- **`domain` is not enrichment.** The domain is derived locally from the saved URL
  the moment the item is saved. `title`, `platform`, `description` and `image_url`
  are enrichment metadata, because they are facts about the page at its source
  rather than about the URL.
- **Every metadata field is optional.** A page may expose only some of them, or
  none. A product page with a title and image but no description is normal, and a
  page that exposes nothing usable is normal too. Missing metadata is not a
  defect in the Saved Item and not a defect in enrichment.
- **`completed` means the enrichment process succeeded**, not that every field was
  found. An item whose page exposed a title and nothing else is `completed`. An
  item whose page exposed nothing at all may also be `completed`. The status
  describes the process, not the fullness of the result.
- **`failed` means the enrichment process itself failed** — the page could not be
  retrieved or read at all. A `failed` item keeps its URL, its domain, its place
  in its collection, and its presence in search by URL and domain.
- **Enrichment never invalidates a Saved Item.** The URL is the user's real data.
  A page that times out, blocks the request, has been deleted, or simply has
  nothing to say must never make the item disappear, be rejected, or be considered
  broken.
- **Enrichment does not organize.** It never decides a collection and never moves
  an item between collections. An item stays in the collection the user chose, or
  in `Unsorted`, regardless of enrichment state. Automatically filing items into
  collections is a separate concern, driven by what enrichment found rather than
  by enrichment itself.
- **A successful enrichment may file the item, as a separate later step.**
  When enrichment completes and reports a platform, a background step may move the
  Saved Item into a collection named after that platform. It is a distinct step
  with its own rules, and it never changes what enrichment recorded: an item whose
  organization failed, was skipped, or was never needed is still `completed`.
  Organization also only ever moves an item that is still in `Unsorted`, so it
  can never undo where the user filed something.
- **Where metadata comes from.** Titles, descriptions and images come from what
  the page declares about itself, and where a page offers several such statements
  the more specific one wins. `platform` is only ever taken from what the page
  claims to be — never guessed from which site the URL happens to point at, since
  a link to an article hosted elsewhere is not made by that host.

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

### Automatic organization

After a background enrichment completes and reports a platform, a separate
background step may file the Saved Item into a collection named after that
platform. It is automatic, not invisible, and it is deliberately narrow. It is
also the only thing that decides a Saved Item's collection automatically; a user
filing an item is always a separate, deliberate act.

- **It only acts on what is true when it runs.** If a collection named like the
  platform already exists, the item goes there, whether the user named it or an
  earlier item created it. Nothing is created beside it, and nothing is renamed.
- **It only files items that are still in `Unsorted`.** An item the user has
  already filed into a collection of their own is left exactly where it is, even
  if the step runs long after the enrichment that could have filed it. The user's
  choice is the newer decision, and automatic organization never undoes it.
- **A collection it creates belongs to that user.** "Created automatically" says
  where it came from, not who owns it: it belongs to one user, like any other, and
  is never shared between users.
- **It can be best-effort without the user ever noticing a problem.** A Saved Item
  whose organization was skipped or never ran keeps its URL, its enrichment, and
  its collection, and is exactly as valid as one that was filed. Nothing about
  organization appears in the enrichment state, so a Saved Item reads as
  `completed` whether or not it was ever organized.
- **Repeating it does nothing.** A second attempt on an already-organized item
  changes no collection and creates no second one.

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
and the saved item enrichment state columns. Enrichment is reachable both ways: a
user can ask for one of their own saved items to be enriched and see the result
straight away, and every save is enriched automatically afterwards in the
background.

### Metadata extraction

How a Saved Item's metadata is obtained, without exposing how it is stored or
scheduled.

- Retrieval never happens during a save. Saving a URL does not fetch the page, so
  a slow or unreachable source can never delay or break a save. Metadata arrives
  only afterwards, either because the save scheduled it or because a user asked.
- A page is read as a document, and its own declarations are preferred in order of
  specificity: a description written for sharing beats a generic one, and a title
  declared for the page beats one padded with a site name.
- Extracted text is tidied of the whitespace markup introduces, and otherwise left
  alone. A title is something a person reads, so it is not lowercased,
  shortened, or rewritten.
- Relative image and canonical links are made absolute against the page they were
  found on, so a stored reference actually identifies something.
- A page that is not a readable HTML page — an image, a PDF, an archive, or a
  response reporting an error — yields no metadata rather than a wrong guess. It
  is not treated as a partially successful extraction.
- One unreadable block on an otherwise fine page, such as malformed structured
  data, does not cost the page the metadata that was readable around it.

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
3. Metadata enrichment + automatic organization _(done: on-demand and automatic
   background)_
4. Reminder / return loop
5. Product metadata enrichment
6. Comparison
7. Optional AI
8. Optional screenshot / media storage
9. Advanced price / decision features
10. Customer web / advanced infrastructure
