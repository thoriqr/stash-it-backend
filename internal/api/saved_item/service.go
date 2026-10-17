package saved_item

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	saveditemdb "github.com/thoriqr/stash-it-backend/internal/api/saved_item/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/pagination"
)

type SavedItemService interface {
	Create(
		ctx context.Context,
		userID uuid.UUID,
		rawURL string,
	) (CreateResult, error)

	Get(
		ctx context.Context,
		userID uuid.UUID,
		savedItemID uuid.UUID,
	) (GetResult, error)

	Delete(
		ctx context.Context,
		userID uuid.UUID,
		savedItemID uuid.UUID,
	) (DeleteResult, error)

	// List returns one page of the user's recent saved items across every collection
	// they own, newest first, plus the cursor state that follows the page.
	List(
		ctx context.Context,
		userID uuid.UUID,
		limit int,
		cursor string,
	) (ListSavedItemsResult, error)
}

type service struct {
	repository Repository
	enqueuer   SavedItemEnqueuer
	log        *zap.Logger
}

// NewService builds the saved item service.
//
// enqueuer may be nil. Background enrichment is an enhancement to the core loop,
// so a save must still succeed when the queue cannot be reached at all, and a nil
// enqueuer is how "this process schedules nothing" is expressed without a second
// code path. log is required because the one failure that cannot be returned to
// the caller — an unreachable queue after an already committed save — has to be
// recorded somewhere, and the saved item itself must not carry that detail.
func NewService(
	repository Repository,
	enqueuer SavedItemEnqueuer,
	log *zap.Logger,
) *service {
	return &service{
		repository: repository,
		enqueuer:   enqueuer,
		log:        log,
	}
}

// CreateResult reports the saved item a save committed.
//
// The projection is the complete saved item, but the create response deliberately
// reports only id, url, collection_id and enrichment_status. At this point in a
// save's life domain is the only metadata column with a value, and it was derived
// locally from the URL rather than read off the page, so returning the rest would
// be a set of nulls presented as though they had been looked up.
type CreateResult struct {
	SavedItem SavedItem
}

// Create saves a URL as a saved item and then schedules its enrichment.
//
// The order of those two halves is the design, and it is not interchangeable:
//
//  1. The row is written. domain is derived locally and nothing contacts the
//     remote source, so a slow or unreachable page can never delay or fail a
//     save.
//  2. Only once that write has returned successfully is enrichment queued.
//
// The write is a single statement in autocommit, so a nil error from the
// repository means it committed. Enqueueing before the write returned would put a
// task in Redis for an id that may never reach the database, and a worker would
// then go looking for a row that does not exist. Enqueueing after it returned
// makes the worst case the opposite one and far smaller: a committed row whose
// task was never queued, which is a valid saved item that still says pending and
// can be enriched on demand.
//
// A failure to enqueue is logged and swallowed. The row exists; returning an
// error to the client would report a failed save for a save that happened, and
// adding a fallback would mean this request performing the network work the queue
// exists to keep out of it.
func (s *service) Create(
	ctx context.Context,
	userID uuid.UUID,
	rawURL string,
) (CreateResult, error) {
	domain, err := deriveDomain(rawURL)
	if err != nil {
		return CreateResult{}, err
	}

	// platform here is the semantic identity of the content, such as the video
	// platform an item was saved from. It is NOT the client platform that
	// sessions.platform records from X-Platform, so no client header is read.
	//
	// Unlike domain, which is technical URL data known locally from the
	// submitted URL alone, platform is metadata about what the page actually is.
	// It is therefore not determined here and stays NULL until the background
	// enrichment process reads it off the remote page. Deriving a guess from the
	// hostname here would be neither reliable nor the product's definition of the
	// field, so no attempt is made to fill it in.
	//
	// title is left NULL for the same reason: it is not client supplied and does
	// not exist locally.
	//
	// Every saved item belongs to exactly one collection, and a newly saved item
	// belongs to the authenticated user's Unsorted collection. It is resolved from
	// the same user_id that comes from the access token, so an item can never be
	// filed under another user's collection. Migration 000022 seeded Unsorted for
	// every existing user; nothing is created here.
	collectionID, err := s.repository.GetUnsortedCollectionByUser(ctx, userID)
	if err != nil {
		return CreateResult{}, err
	}

	savedItem, err := s.repository.CreateSavedItem(
		ctx,
		saveditemdb.CreateSavedItemParams{
			UserID: userID,
			Url:    rawURL,
			Domain: optionalText(domain),
			// platform is deliberately left unset for enrichment to populate.
			// An empty pgtype.Text is SQL NULL, which is the intended state.
			Platform:     pgtype.Text{},
			Title:        pgtype.Text{},
			CollectionID: collectionID,
		},
	)
	if err != nil {
		return CreateResult{}, err
	}

	s.enqueueEnrichment(ctx, savedItem.ID)

	return CreateResult{
		SavedItem: savedItem,
	}, nil
}

// enqueueEnrichment queues background enrichment for a saved item that is
// already committed.
//
// It runs after the write and never before, which is the transaction boundary
// this feature cares about: CreateSavedItem is a single autocommit statement, so
// the row exists by the time this is called, and there is no window in which a
// queued task could name an id the database never accepted.
//
// A failure is logged and not returned. The saved item is already durable and is
// valid product data regardless of whether its metadata was ever fetched, so the
// item is returned as a normal successful save. The log is what distinguishes the
// two outcomes afterwards, which is the only place that distinction can live:
// enrichment_status stays pending either way, and there is no column for it.
func (s *service) enqueueEnrichment(
	ctx context.Context,
	savedItemID uuid.UUID,
) {
	if s.enqueuer == nil {
		return
	}

	if err := s.enqueuer.EnqueueSavedItemEnrichment(
		ctx,
		savedItemID,
	); err != nil {
		s.log.Warn(
			"saved item saved but background enrichment was not queued",
			zap.String("saved_item_id", savedItemID.String()),
			zap.Error(err),
		)
	}
}

type GetResult struct {
	SavedItem SavedItem

	// Collection is the collection the item is filed in, named. It comes from the
	// same statement that read the item, so reporting it costs nothing, and a caller
	// opening one item can see where it lives without a second request.
	Collection SavedItemCollection
}

func (s *service) Get(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) (GetResult, error) {
	savedItem, collection, err := s.repository.GetSavedItemByIDForUser(
		ctx,
		userID,
		savedItemID,
	)
	if err != nil {
		return GetResult{}, err
	}

	// The item is returned whatever state its metadata is in. A pending or failed
	// enrichment is an ordinary state of a saved item, not a reason to withhold it,
	// so there is no branch here on enrichment_status.
	return GetResult{
		SavedItem:  savedItem,
		Collection: collection,
	}, nil
}

// DeleteResult reports what the deletion left behind in the collection the saved
// item was in.
//
// Every field is information for the caller and nothing more. The delete removed
// exactly one saved item and this service never deletes or modifies a collection,
// so a collection that became empty stays, empty, until the caller separately
// decides to delete it.
type DeleteResult struct {
	// CollectionID is the collection the deleted saved item was actually in.
	CollectionID uuid.UUID

	// CollectionEmpty reports whether that collection held no other saved item
	// once this one was gone. It is a fresh observation taken after the delete
	// committed, not a guarantee about anything that happens next.
	CollectionEmpty bool

	// CollectionDeletable reports whether the user may now delete that
	// collection, which is CollectionEmpty and the collection not being the
	// protected Unsorted one.
	//
	// It is a convenience for the caller, not an authorization decision and not a
	// concurrency guarantee. Deleting a collection is a separate explicit
	// operation that re-reads the database and validates again, so this value
	// being wrong costs at most a rejected or unnecessary request.
	CollectionDeletable bool
}

// Delete removes one saved item of the authenticated user and reports what it
// left behind in the collection the item was in.
//
// The collection is never touched. Deleting a saved item and deleting a
// collection are separate operations with separate endpoints, and this one only
// describes the collection so the caller can decide what to do about it. That is
// why the answer is reported rather than acted on: the caller asked to remove an
// item, and a collection holding nothing is a valid state this product already
// has, so quietly removing one would take an action nobody requested.
//
// There is deliberately no transaction around the three statements below, and the
// reason is that there is nothing to keep consistent. The delete is a single
// autocommit statement, so by the time it returns the row is committed and gone.
// The two statements after it only read what that delete left behind. Wrapping
// them would not make the answer any more accurate, because under READ COMMITTED
// each statement takes its own snapshot regardless of whether they share a
// transaction, and it would add a pool and a transaction boundary to a feature
// that currently has neither for one observation. List already composes a derived
// count from a second statement the same way.
//
// Every statement is scoped by the user, so a saved item belonging to somebody
// else fails on the delete and the collection is never read. A missing item and a
// foreign one both return the same not found error, which is what keeps the
// endpoint from disclosing whether an ID exists for someone else.
func (s *service) Delete(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) (DeleteResult, error) {
	deleted, err := s.repository.DeleteSavedItemByIDForUser(
		ctx,
		userID,
		savedItemID,
	)
	if err != nil {
		return DeleteResult{}, err
	}

	remaining, err := s.repository.CountSavedItemsInCollection(
		ctx,
		userID,
		deleted.CollectionID,
	)
	if err != nil {
		return DeleteResult{}, err
	}

	// Read after the delete rather than before it, and after the count, so the
	// identity is the one belonging to the row that is actually gone.
	systemKey, err := s.repository.GetCollectionSystemKeyForUser(
		ctx,
		userID,
		deleted.CollectionID,
	)
	if err != nil {
		return DeleteResult{}, err
	}

	// Valid is checked before the value, so an absent key is never compared. A
	// collection with no key is one the user created, and comparing an unset value
	// would mean deciding what an empty identity is.
	isUnsorted := systemKey.Valid &&
		systemKey.String == CollectionSystemKeyUnsorted

	collectionEmpty := remaining == 0

	return DeleteResult{
		CollectionID:        deleted.CollectionID,
		CollectionEmpty:     collectionEmpty,
		CollectionDeletable: collectionEmpty && !isUnsorted,
	}, nil
}

// List returns one page of the user's recent saved items, newest first.
//
// This is the recent history: every saved item the caller owns, across every
// collection they own. It is deliberately not a view of one collection, and it is
// deliberately not a filtered subset.
//
// The three rules it owns are the same ones every other listing in this repository
// owns, and each exists because the alternative would be guessing:
//
//   - The limit is defaulted and clamped here, so the bound is stated once rather
//     than per query and a caller reaching this service without going through the
//     request type still gets the same page size.
//   - The cursor is decoded, validated and its position parsed here. The repository
//     receives a position, never a token.
//   - The next cursor is built here rather than in the repository, from the last row
//     actually returned.
//
// The cursor is settled before anything else runs. A token is a string this build did
// not write, so it cannot be trusted to hold a usable position, and rejecting one
// without spending a query or taking a connection is the difference between a 400
// and a 500 that a client cannot act on.
//
// Nothing here filters on enrichment_status, and there is no branch on how far
// enrichment or automatic organization got. An item whose page has not been read, an
// item whose page could not be read and an item that has been filed into a collection
// are all ordinary states of a saved item, and all of them happened. A history that
// hid the two it had not finished processing would be a record of only what went
// well. Each item reports its own enrichment_status, so a client can tell the states
// apart without any of them being withheld.
//
// This method reads and nothing else. It holds no enqueuer, opens no transaction and
// writes nothing: enrichment is scheduled by saving a URL and by an explicit request
// for one item, and looking at the history is not a reason to fetch anything.
func (s *service) List(
	ctx context.Context,
	userID uuid.UUID,
	limit int,
	cursor string,
) (ListSavedItemsResult, error) {
	if limit <= 0 {
		limit = SavedItemListDefaultLimit
	}

	if limit > SavedItemListMaxLimit {
		limit = SavedItemListMaxLimit
	}

	params := ListSavedItemsParams{
		UserID: userID,
		Limit:  limit,
	}

	if cursor != "" {
		decoded, err := decodeListSavedItemsCursor(cursor)
		if err != nil {
			return ListSavedItemsResult{}, err
		}

		params.Cursor = decoded
	}

	result, err := s.repository.ListSavedItems(ctx, params)
	if err != nil {
		return ListSavedItemsResult{}, err
	}

	// The next cursor is derived from the last item actually returned, never the
	// lookahead row: a cursor built from a row the client never received would resume
	// past it and skip one item at every page boundary.
	if result.HasMore && len(result.SavedItems) > 0 {
		token, err := encodeListSavedItemsCursor(result.SavedItems)
		if err != nil {
			return ListSavedItemsResult{}, apperror.Internal(err)
		}

		result.NextCursor = &token
	} else {
		// A final page and an empty page are the same shape, which the response
		// renders as an explicit null rather than omitting the field.
		result.HasMore = false
		result.NextCursor = nil
	}

	return result, nil
}

// decodeListSavedItemsCursor turns an opaque token into a validated cursor.
//
// Every failure is the same 400 with the same code: a cursor is not a capability, so
// "malformed" and "unusable" mean one thing to a caller, which is to start over
// without a cursor.
//
// There is no sort or group to check. This listing has one ordering and no pinned
// row, so a token cannot be mismatched against anything.
//
// Both halves of the position are required. An ordering on created_at alone is not
// total, so a position without the tie-breaker cannot describe where a page ended,
// and reading one as the top of the history would silently return the newest page to
// a caller that asked to resume.
func decodeListSavedItemsCursor(token string) (*ListSavedItemsCursor, error) {
	decoded, err := pagination.DecodeVersioned[ListSavedItemsCursor](token)
	if err != nil {
		return nil, invalidCursorError(err)
	}

	if decoded.Value == nil || decoded.ID == nil {
		return nil, invalidCursorError(nil)
	}

	if *decoded.Value == "" || *decoded.ID == uuid.Nil {
		return nil, invalidCursorError(nil)
	}

	// The position is compared against a timestamptz column, so it has to actually be
	// a timestamp. Checking it here rather than in the repository turns an unusable
	// token into the 400 it is before any query runs: the value arrives as a string
	// because that is what JSON carries, and only this layer knows the payload's
	// shape well enough to say whether a string is well formed.
	//
	// The parsed form is recorded on the cursor, so the repository receives the type
	// sqlc expects and has nothing left to parse or reinterpret.
	parsed, err := time.Parse(time.RFC3339Nano, *decoded.Value)
	if err != nil {
		return nil, invalidCursorError(err)
	}

	decoded.Timestamp = pgtype.Timestamptz{Time: parsed, Valid: true}

	return &decoded, nil
}

// invalidCursorError reports that a cursor cannot be used.
//
// The underlying cause is kept as the wrapped error for the log rather than the
// message, since the message is what reaches anything rendering the response and a
// cursor's internal shape is not something to describe there.
func invalidCursorError(cause error) error {
	return apperror.BadRequestWith(
		CodeInvalidCursor,
		"cursor is invalid",
		cause,
	)
}

// encodeListSavedItemsCursor builds the token for the page ending at the given item.
//
// The position is read from the item as stored, never re-derived, so the value in the
// token is the one the query ordered by. RFC3339Nano is used because it round-trips a
// time.Time exactly and is the only lossless textual form available for one.
func encodeListSavedItemsCursor(savedItems []ListedSavedItem) (string, error) {
	last := savedItems[len(savedItems)-1]

	value := last.CreatedAt.Time.Format(time.RFC3339Nano)

	return pagination.Encode(ListSavedItemsCursor{
		Version: pagination.CurrentVersion,
		Value:   &value,
		ID:      &last.ID,
	})
}

// deriveDomain extracts and normalizes the host from the submitted URL. It never
// contacts the remote page: parsing is purely local, which keeps saving fast and
// free.
//
// Phase A normalization is deliberately minimal: lowercase the host and remove a
// single leading "www.". Other subdomains (m., shop., ...) are preserved as-is,
// and no public suffix or registrable-domain detection is performed.
func deriveDomain(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", apperror.BadRequestWith(
			CodeInvalidURL,
			"url is invalid",
			err,
		)
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return "", apperror.BadRequestWith(
			CodeInvalidURL,
			"url must include a host",
			nil,
		)
	}

	domain := strings.TrimPrefix(strings.ToLower(hostname), "www.")
	if domain == "" {
		return "", apperror.BadRequestWith(
			CodeInvalidURL,
			"url must include a host",
			nil,
		)
	}

	return domain, nil
}

func internalError(err error) error {
	return apperror.Internal(err)
}
