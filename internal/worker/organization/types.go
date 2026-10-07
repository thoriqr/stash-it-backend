package organization

import (
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// SavedItem is the state an organization decision is made on, read at execution
// time rather than carried in the task.
//
// Every field is read fresh. None of it is taken from the payload, because the
// payload says which item to look at, not what is true about it now. In
// particular CollectionSystemKey is read at the moment of the decision so a user
// action that happened while the task waited is visible to it.
//
// This is a narrower projection than either the API collection feature's or the
// enrichment worker's, and narrower than the table. It carries only what the
// decision reads and nothing that organization may write: the metadata
// enrichment produced, including the platform's rendered value and the
// timestamps, are deliberately absent so that no code path here can reach them.
type SavedItem struct {
	ID                  uuid.UUID
	UserID              uuid.UUID
	EnrichmentStatus    string
	Platform            pgtype.Text
	CollectionID        uuid.UUID
	CollectionSystemKey pgtype.Text
}

// IsInUnsorted reports whether the item is currently in the user's Unsorted
// collection.
//
// This is the guard that makes automatic organization additive rather than
// authoritative. An item the user has filed into a collection of their own is
// skipped, so a task queued before the user acted never moves the item back.
//
// It matches on the collection's system_key rather than its display name, because
// system_key is a collection's stable identity: Unsorted could be renamed by a
// future product change and this decision would still be right. It is also what
// distinguishes the real Unsorted from a collection the user named "Unsorted"
// themselves — collections_system_key_check gives every user collection a NULL
// system_key, so a user collection can share the name without sharing the key.
func (s SavedItem) IsInUnsorted() bool {
	return s.CollectionSystemKey.Valid &&
		s.CollectionSystemKey.String == CollectionSystemKeyUnsorted
}

// HasPlatform reports whether enrichment produced a platform worth organizing
// for.
//
// A page that exposed nothing, or that named itself with something enrichment
// deliberately refused to treat as a platform, leaves this false and there is
// nothing to organize on. NULL is the absence of a decision, not a decision to
// leave the item alone.
func (s SavedItem) HasPlatform() bool {
	return s.Platform.Valid && s.Platform.String != ""
}

// IsEnrichmentCompleted reports whether enrichment succeeded for this item.
//
// Organization is downstream of enrichment and depends on its result, so an item
// whose enrichment failed or has not run has no platform to organize on. This is
// read rather than inferred from the platform's presence because a failed
// enrichment preserves whatever metadata the item already had, which means a
// platform can be present on an item whose latest enrichment failed.
func (s SavedItem) IsEnrichmentCompleted() bool {
	return s.EnrichmentStatus == "completed"
}

// Collection is a collection of one user, as organization needs to see one.
//
// The type and system_key are read because they are what make the reuse decision
// safe and explainable: an automatically created collection and one the user named
// are both legitimate targets, and the difference between them is the user's to
// have chosen.
type Collection struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Name      string
	Type      string
	SystemKey pgtype.Text
}

// Collection types, mirroring the schema's collections_type_check.
//
// 'system' describes where a collection came from, not who owns it. A system
// collection belongs to exactly one user through collections.user_id, exactly as a
// user collection does.
const (
	CollectionTypeSystem = "system"
	CollectionTypeUser   = "user"
)

// IsSystem reports whether the collection was created by automatic organization
// rather than named by the user.
//
// It is informational. Organization does not decide differently based on it: a
// collection of either type that matches the platform is a valid target, and
// reusing a user-named one is the behaviour rather than an exception to it.
func (c Collection) IsSystem() bool {
	return c.Type == CollectionTypeSystem
}

// SystemKeyForPlatform derives the stable identity of a platform collection from
// the platform value enrichment produced.
//
// The key is the platform lowercased and trimmed, which is the same comparison
// the name index uses, so the key and the name can never disagree about which
// platform a collection belongs to: two values that map to one key are also two
// values the name index considers the same.
//
// It is a derived identity rather than a curated registry, and that is a real
// limitation worth stating. The platform is free text taken from what the page
// claimed about itself, so "YouTube" and "youtube" collapse to one collection
// while "YouTube Music" is a different one. That is the correct behavior for a
// value whose only authority is the page that published it, and deriving a
// curated mapping instead would mean inferring a platform from something other
// than what the page says.
func SystemKeyForPlatform(platform string) string {
	return normalizeCollectionName(platform)
}

// Outcome describes what an organization attempt actually did.
//
// Every value here is a successful completion of the task. They exist so the
// worker can log the specific reason it did nothing, which is the difference
// between "the user already filed this" and "enrichment produced no platform"
// when someone is diagnosing why an item is still in Unsorted.
type Outcome int

// OutcomeUnknown is the zero value: a call that failed before deciding anything.
// It is deliberately not a meaningful outcome, so a caller that ignores an error
// cannot mistake it for one of the no-ops below and report success.
const OutcomeUnknown Outcome = iota

const (
	// OutcomeMoved means the item was filed into an existing platform collection.
	OutcomeMoved Outcome = iota + 1

	// OutcomeCollectionCreated means the item was filed into a collection this
	// attempt had to create.
	OutcomeCollectionCreated

	// OutcomeAlreadyOrganized means the item was already in the target collection
	// when the task ran, so no write was needed. This is what a duplicate delivery
	// of the same task produces.
	OutcomeAlreadyOrganized

	// OutcomeNotInUnsorted means the item was already in a collection the user
	// chose, so it was left exactly where it is.
	OutcomeNotInUnsorted

	// OutcomeNoPlatform means enrichment produced no platform to organize on.
	OutcomeNoPlatform

	// OutcomeEnrichmentNotCompleted means the item's enrichment had not succeeded,
	// so there is no fresh platform to organize on.
	OutcomeEnrichmentNotCompleted
)

// String names the outcome for logging.
func (o Outcome) String() string {
	switch o {
	case OutcomeMoved:
		return "moved"
	case OutcomeCollectionCreated:
		return "collection created"
	case OutcomeAlreadyOrganized:
		return "already organized"
	case OutcomeNotInUnsorted:
		return "not in unsorted"
	case OutcomeNoPlatform:
		return "no platform"
	case OutcomeEnrichmentNotCompleted:
		return "enrichment not completed"
	default:
		return "unknown"
	}
}

// DidMove reports whether the outcome wrote to saved_items.
func (o Outcome) DidMove() bool {
	return o == OutcomeMoved || o == OutcomeCollectionCreated
}

// normalizeCollectionName reduces a collection name the way the schema compares
// one.
//
// This mirrors the lower(btrim(name)) the unique index and the lookup statement
// use. A second, different normalization would let organization decide two names
// are different when the database considers them the same, which would produce
// either a duplicate collection or a constraint violation.
//
// The database remains the authority: both the lookup and the conflict target are
// expressed in SQL as lower(btrim(name)), so this value is only ever used to
// derive a system_key and to report what was compared.
func normalizeCollectionName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
