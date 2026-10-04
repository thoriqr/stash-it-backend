# Discovery — Product Brief

Purpose of this document: give developers and coding agents a concise picture of
what Discovery is and what is being built right now, so that work stays aligned
with the current phase instead of drifting toward the long-term vision.

## What Discovery is

Discovery is a personal place for the things a user finds on the internet. They
save an item, and they can find it and return to it later.

The whole product exists to shorten the distance between _finding something
worth keeping_ and _actually going back to it_. Everything else is either
supporting that path or optional.

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

The loop only has one optional step. Saving must never depend on organizing,
and organizing must never be a precondition for keeping something.

## The core object

Everything in Discovery centers on one object: a **Saved Item**, identified by a
URL and enriched with metadata from the original source.

A Saved Item should feel like a useful preview of what the user saved, not just a
stored URL.

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

| Field           | Notes                                                     |
| --------------- | --------------------------------------------------------- |
| `id`            |                                                           |
| `user_id`       | owner, always from authentication, never from the client   |
| `url`           | the thing saved, supplied by the client                    |
| `domain`        | derived from the URL, never fetched from the remote source |
| `platform`      | content/source platform, null until enrichment runs        |
| `title`         | null until enrichment runs                                 |
| `created_at`    |                                                           |
| `updated_at`    |                                                           |
| `collection_id` | owning collection, required in the database                |

### Current Saved Item behavior

- `user_id` comes from authentication, never from the request.
- `url` comes from the client.
- `domain` is derived server-side from the submitted URL. Normalization
  lowercases the hostname and removes one leading `www.` prefix. Other subdomains
  are preserved, and no public suffix or registrable-domain detection is
  performed.
- `platform` stays null until enrichment runs. It is the **content/source**
  platform (youtube, tiktok, instagram, pinterest, ...) and is never derived from
  the client's platform header.
- `title` stays null until enrichment runs.
- `collection_id` points at the collection the item belongs to and is required.
  Every user has an `Unsorted` system collection, and a newly saved item lands
  there. An item can then be moved into a collection of the user's own.
- `enrichment_status` starts at `pending` and is only ever advanced by the later
  background enrichment process, which does not exist yet.

`X-Platform` describes the client or device platform (web/android/ios). It is a
session concern and must never be used for `saved_items.platform`. The two fields
mean different things and are not interchangeable.

Saving never contacts the remote source. Domain derivation is purely local, so
saving stays fast and free.

Enrichment of `platform` and `title` is a later background process that runs
after a save and never blocks it.

### The Unsorted collection

`Unsorted` is the default collection every user starts with. It exists so that
saving never depends on organizing: a new account can save immediately, and its
inbox begins as one unsorted pile instead of an empty requirement.

- When a registration is finalized and the account becomes permanent, that user
  receives their `Unsorted` collection.
- A registration that is still pending has no account yet and no collections.
- Every newly saved item belongs to the user's `Unsorted` collection.
- `Unsorted` is allowed to stay empty; it is never required to hold anything,
  and it is never removed when its items move elsewhere.

### Collections

`Unsorted` is where saving lands, and the user's own collections are where items
go once they have been organized. Organizing is optional: an item that has never
been moved stays in `Unsorted`, and that is a normal state, not a problem.

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
- Metadata extraction is unaffected. Organizing an item never waits on, blocks,
  or alters enrichment, and an item can be moved whatever state its metadata is
  in.

## Current phase: Phase B — basic organization and return

Phase A gave a complete, reliable path from _I saw something_ to _it is saved and
waiting in my Inbox_: authentication, saving a URL, and the Inbox.

Phase B turns a storage bucket into something a user returns to. It is what
makes saved items findable again later.

Implemented:

- Authentication
- Save a URL
- Inbox
- Saved item detail
- Delete a saved item
- Collections, including filing an item into a collection of the user's own

Still to build:

- Basic search

Saved item detail and delete were built during Phase A because they are needed to
complete the core loop, and Collections followed once the loop was solid. What
genuinely remains for Phase B is **basic search**: without it, an item saved
weeks ago is only findable if the user remembers which collection it went into.

The database foundation is in place: the `collections` table, the
`saved_items.collection_id` relationship, and the saved item enrichment state
columns. The enrichment columns exist but nothing populates them yet; metadata
extraction remains a later phase.

Not part of this phase, by decision:

- Editing a saved item is intentionally not implemented. Nothing in the core
  loop requires it, so it is not scheduled ahead of the work below.
- Opening the original URL is a client-side behavior, outside the current backend
  scope.

## Out of scope for the core MVP

These are excluded on purpose. Their absence is a decision, not a gap.

- AI
- Comparison
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
  return later. If a change does not serve that path, it needs justification.
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
- **No complexity before the core proves useful.** Resist adding features because
  they seem like they belong in the category.
- **The final vision is not the finish line.** A working product covering share,
  save, collections, search, and metadata is a valid and successful outcome. The
  product does not need to reach its most advanced state to succeed.

## Development order

1. Share → Save → Inbox *(done)*
2. Collections + Search *(Collections done, Search next)*
3. Background metadata extraction
4. Reminder / return loop
5. Product metadata enrichment
6. Comparison
7. Optional AI
8. Optional screenshot / media storage
9. Advanced price / decision features
10. Customer web / advanced infrastructure

Work should follow this sequence. Jumping ahead is the most common way this
project goes wrong.
