# Discovery — Product Brief

Purpose of this document: give developers and coding agents a concise picture of
what Discovery is, what it is for, and where it is heading, so that work stays
aligned with the product instead of drifting toward the long-term vision. How the
product behaves, feature by feature, is in `product-behavior.md`.

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
- product price, when the source page publishes one

Preview images are references to images provided by the original source. Discovery
does not own or guarantee the availability of those external images.

A product price is a snapshot of what the page reported when it was last read. It
may be absent, and it is not tracked, updated, or compared over time.

Enrichment metadata is additive. A Saved Item is a valid, useful saved item
before any of it arrives, and it stays one when none of it does.

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

## Out of scope for the core MVP

These are excluded on purpose. Their absence is a decision, not a gap.

- AI
- Screenshot / media cloud storage
- Price tracking
- Customer web workspace
- Social features

## Roadmap

The order of work, in rough order of leverage. It is an order, not a status report.
The first three steps are the core product; everything after them is optional and
additive, and none of it is a prerequisite for the core loop.

1. **Share → Save → Inbox.** Authentication, saving a URL, and the Inbox.
2. **Collections and search.** Saved items become findable and organizable again.
3. **Metadata enrichment and automatic organization.** Items read as previews and
   can be filed into collections automatically.
4. Reminder / return loop
5. Product metadata enrichment (richer product metadata)
6. Comparison
7. Optional AI (AI-assisted extraction or comparison)
8. Optional screenshot / media storage
9. Advanced price / decision features (price tracking and alerts)
10. Customer web / advanced infrastructure

Internal admin capabilities are also later and have no fixed place in this order.
