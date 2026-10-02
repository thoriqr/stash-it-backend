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

| Field           | Notes           |
| --------------- | --------------- |
| `id`            |                 |
| `user_id`       | owner           |
| `url`           | the thing saved |
| `domain`        |                 |
| `platform`      |                 |
| `title`         | optional        |
| `created_at`    |                 |
| `updated_at`    |                 |
| `collection_id` | optional        |

## Current phase: Phase A — core capture

This is what is being built now.

- Share a URL into the app
- Save the URL
- Inbox
- Authentication

The goal is a complete, reliable path from _I saw something_ to _it is saved and
waiting in my Inbox_.

## Next phase: Phase B — basic organization and return

- Collections
- Basic search
- Saved item detail
- Open the original URL
- Delete a saved item

This phase is what turns a storage bucket into something a user returns to.

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
  Anything slow happens afterwards, if at all.
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

1. Share → Save → Inbox
2. Collections + Search
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
