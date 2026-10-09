package collection

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	collectiondb "github.com/thoriqr/stash-it-backend/internal/api/collection/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/pagination"
)

type Service interface {
	// PutSavedItem puts one of the user's saved items into the user collection
	// with the given name, creating that collection if it does not exist yet.
	PutSavedItem(
		ctx context.Context,
		userID uuid.UUID,
		savedItemID uuid.UUID,
		collectionName string,
	) (PutSavedItemResult, error)

	// ListCollections returns one page of the user's collections in the requested
	// order, with the Unsorted collection pinned first, plus the cursor state that
	// follows the page.
	ListCollections(
		ctx context.Context,
		userID uuid.UUID,
		sort CollectionSort,
		limit int,
		cursor string,
	) (ListCollectionsResult, error)

	// ListSavedItemsInCollection returns one page of the saved items in one of the
	// user's collections, newest first, plus the cursor state that follows the page.
	ListSavedItemsInCollection(
		ctx context.Context,
		userID uuid.UUID,
		collectionID uuid.UUID,
		limit int,
		cursor string,
	) (ListSavedItemsInCollectionResult, error)

	// DeleteCollection removes one of the user's collections and applies the
	// disposition of its saved items that the caller chose.
	DeleteCollection(
		ctx context.Context,
		params DeleteCollectionParams,
	) error
}

type service struct {
	repository Repository
}

func NewService(
	repository Repository,
) *service {
	return &service{
		repository: repository,
	}
}

// PutSavedItemResult reports what the operation did.
//
// CollectionCreated and AlreadyInCollection are mutually exclusive: a collection
// that was just created cannot already hold the saved item.
type PutSavedItemResult struct {
	Collection          collectiondb.Collection
	CollectionCreated   bool
	AlreadyInCollection bool
	SavedItem           SavedItem
}

// PutSavedItem puts a saved item into the user's collection with the given name,
// creating the collection if it does not exist yet.
//
// The operation is idempotent. When the saved item is already in the target
// collection nothing is written, and this is reported through AlreadyInCollection
// rather than reported as a conflict.
//
// The service owns only the name rules and the single call into the repository.
// The transaction, the create-or-get and the move all belong to the repository,
// because they cannot be split without risking a collection that holds no item.
func (s *service) PutSavedItem(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
	collectionName string,
) (PutSavedItemResult, error) {
	name, err := normalizeCollectionName(collectionName)
	if err != nil {
		return PutSavedItemResult{}, err
	}

	result, err := s.repository.PutSavedItemIntoUserCollection(
		ctx,
		PutSavedItemIntoUserCollectionParams{
			UserID:      userID,
			SavedItemID: savedItemID,
			Name:        name,
		},
	)
	if err != nil {
		return PutSavedItemResult{}, err
	}

	return PutSavedItemResult{
		Collection:          result.Collection,
		CollectionCreated:   result.CollectionCreated,
		AlreadyInCollection: result.AlreadyInCollection,
		SavedItem:           result.SavedItem,
	}, nil
}

// ListCollections returns one page of the user's collections.
//
// The service owns three rules here, and each exists because the alternative would
// be guessing or because two implementations could disagree:
//
//   - The sort is validated against the supported set rather than passed through, so
//     an unknown value is a 400 and never reaches a query. It is deliberately not a
//     validate tag on the request, because the same rule must run again against a
//     cursor's own sort value: a token is a string this build did not write.
//   - The limit is defaulted and clamped here, mirroring the saved item and session
//     list services, so the bound is stated once rather than per query.
//   - The cursor is decoded, validated and resolved into either a group or a
//     position. The repository receives a position, never a token.
//
// A cursor is a position, not a lookup. The token records where a page ended and the
// next query resumes from there, so a cursor keeps working after the row it came
// from is deleted: it simply continues with whatever sorts after that position. That
// is the property that separates this from an offset, where deleting a row shifts
// every later row and makes a client receive a duplicate.
//
// Unsorted is not special-cased here. It is not moved to the front by this service,
// not given a name, and not filtered. The ordering that pins it is expressed once in
// SQL as a rank over system_key, so all three sorts get it from the same rule.
func (s *service) ListCollections(
	ctx context.Context,
	userID uuid.UUID,
	sort CollectionSort,
	limit int,
	cursor string,
) (ListCollectionsResult, error) {
	// An absent sort is the default rather than an error, which is why the request
	// tag is omitempty and this resolves it. Resolution happens before validation so
	// the repository is only ever handed a sort it can dispatch on.
	if sort == "" {
		sort = CollectionSortNewest
	}

	if !sort.IsValid() {
		return ListCollectionsResult{}, apperror.BadRequestWith(
			CodeInvalidCollectionSort,
			"sort is not supported",
			nil,
		)
	}

	if limit <= 0 {
		limit = CollectionListDefaultLimit
	}

	if limit > CollectionListMaxLimit {
		limit = CollectionListMaxLimit
	}

	params := ListCollectionsParams{
		UserID: userID,
		Sort:   sort,
		Limit:  limit,
	}

	if cursor != "" {
		decoded, err := decodeListCollectionsCursor(cursor, sort)
		if err != nil {
			return ListCollectionsResult{}, err
		}

		params.Cursor = decoded
	}

	result, err := s.repository.ListCollections(ctx, params)
	if err != nil {
		return ListCollectionsResult{}, err
	}

	// The resolved sort is written back rather than trusted from the repository. The
	// service is what turned an absent sort into a default, so a caller reporting the
	// order it received must see the order that was actually applied, not whatever
	// the row path happened to carry.
	result.Sort = sort

	// The next cursor is derived here rather than in the repository, because encoding
	// is a service concern and the repository deals only in rows. It is built from
	// the last row actually returned, so it never points past a row the caller never
	// received.
	if result.HasMore && len(result.Collections) > 0 {
		token, err := encodeListCollectionsCursor(sort, result.Collections)
		if err != nil {
			return ListCollectionsResult{}, apperror.Internal(err)
		}

		result.NextCursor = &token
	} else {
		// A final page and an empty page are the same shape: no token, no more. The
		// response renders this as an explicit null rather than omitting the field.
		result.HasMore = false
		result.NextCursor = nil
	}

	return result, nil
}

// decodeListCollectionsCursor turns an opaque token into a validated cursor.
//
// Every failure here is the same 400 with the same code. A cursor is not a
// capability, so there is nothing in distinguishing "the token is malformed" from
// "the token was written for another sort" that a caller could act on; both mean
// "start over without a cursor". Telling them apart in the message would only make
// the token's internal shape guessable.
//
// The structural checks exist because the payload is a set of pointers, and every
// one of them can be absent. Group is the load-bearing case: absent, zero and one
// are three different things, and a cursor that cannot say which it is must not be
// allowed to guess, because guessing wrong skips the pinned collection.
func decodeListCollectionsCursor(
	token string,
	requestedSort CollectionSort,
) (*ListCollectionsCursor, error) {
	decoded, err := pagination.DecodeVersioned[ListCollectionsCursor](token)
	if err != nil {
		return nil, invalidCursorError(err)
	}

	if !decoded.Sort.IsValid() {
		return nil, invalidCursorError(nil)
	}

	// A position recorded under one ordering means nothing under another, so a token
	// reused with a different sort is rejected rather than silently misread.
	if decoded.Sort != requestedSort {
		return nil, invalidCursorError(nil)
	}

	if decoded.Group == nil {
		return nil, invalidCursorError(nil)
	}

	switch *decoded.Group {
	case cursorGroupAfterUnsorted:
		// This group means "the page ended on Unsorted", which carries no position.
		// A token claiming both is inconsistent rather than generous.
		if decoded.Value != nil || decoded.ID != nil {
			return nil, invalidCursorError(nil)
		}

	case cursorGroupAfterRow:
		// This group is meaningless without both halves of the position.
		if decoded.Value == nil || decoded.ID == nil {
			return nil, invalidCursorError(nil)
		}

		if *decoded.Value == "" || *decoded.ID == uuid.Nil {
			return nil, invalidCursorError(nil)
		}

	default:
		return nil, invalidCursorError(nil)
	}

	// The time-based sorts compare this value against a timestamptz column, so it has
	// to actually be a timestamp. Checking it here rather than in the repository is
	// what turns an unusable token into the 400 it is, before any query runs: the
	// value arrives as a string because that is what JSON carries, and only this layer
	// knows the payload's shape well enough to say whether a string is well formed.
	//
	// The parsed form is recorded on the cursor, so the repository receives the type
	// sqlc expects and has nothing left to parse or reinterpret.
	if decoded.hasPosition() && requestedSort != CollectionSortName {
		parsed, err := parseCursorTimestamp(*decoded.Value)
		if err != nil {
			return nil, invalidCursorError(err)
		}

		decoded.Timestamp = parsed
	}

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

// encodeListCollectionsCursor builds the token for the page that ends at the given
// row.
//
// The position is read from the row as stored, never re-derived, so the value in the
// token is the one the query ordered by. For the name sort that means the normalized
// name, computed the same way the ordering and the unique index compute it: a token
// carrying the raw display name would compare against a value the index never
// produced and quietly return the wrong rows.
func encodeListCollectionsCursor(
	sort CollectionSort,
	collections []collectiondb.Collection,
) (string, error) {
	last := collections[len(collections)-1]

	group := cursorGroupAfterRow

	// Ending on Unsorted is the one case that needs no position: the next page starts
	// at the top of the regular ordering.
	if isUnsorted(last.SystemKey) {
		group = cursorGroupAfterUnsorted

		return pagination.Encode(ListCollectionsCursor{
			Version: pagination.CurrentVersion,
			Sort:    sort,
			Group:   &group,
		})
	}

	value := formatCursorValue(sort, last)

	payload := ListCollectionsCursor{
		Version: pagination.CurrentVersion,
		Sort:    sort,
		Group:   &group,
		Value:   &value,
		ID:      &last.ID,
	}

	return pagination.Encode(payload)
}

// formatCursorValue renders a row's sort value the way the query ordered by.
//
// Timestamps become RFC3339Nano, which round-trips a time.Time exactly and is the
// only lossless textual form available for one. The name sort stores the normalized
// name, which is already the ordering expression's own output.
func formatCursorValue(sort CollectionSort, collection collectiondb.Collection) string {
	if sort == CollectionSortName {
		return normalizeCollectionNameForCursor(collection.Name)
	}

	return collection.CreatedAt.Time.Format(time.RFC3339Nano)
}

// parseCursorTimestamp turns a stored RFC3339Nano value into the type sqlc expects
// for a timestamptz column.
//
// sqlc.yaml overrides only uuid, so every timestamptz parameter is a
// pgtype.Timestamptz. A failure here is a malformed token rather than a database
// fault, so it is reported as INVALID_CURSOR by the caller. Doing the parse while
// the cursor is still being validated means no query has run and no connection was
// used to discover the token was unusable.
func parseCursorTimestamp(value string) (pgtype.Timestamptz, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return pgtype.Timestamptz{}, err
	}

	return pgtype.Timestamptz{Time: parsed, Valid: true}, nil
}

// normalizeCollectionNameForCursor reduces a display name to the form the ordering
// and collections_user_name_unique both use.
//
// This is the same reduction collection/service.go applies before storing a name, and
// reusing it here is the point: a token whose value disagrees with the expression the
// next query compares it against cannot find its own position.
func normalizeCollectionNameForCursor(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ListSavedItemsInCollection returns one page of the saved items in a collection.
//
// The cursor is validated first, then ownership is proved, then the page is read. The
// order matters twice: an unusable token is refused without spending a query, and a
// collection belonging to another user never reaches an item query. The ownership
// check is the reason this method exists as more than a forwarding call — without it
// the listing would be scoped by collection id alone, and the item query's user_id
// predicate would be the only thing keeping one user's items apart from another's.
//
// A collection belonging to another user and one that does not exist produce the same
// not found error, so this endpoint never discloses whether a collection id exists.
//
// The limit is defaulted and clamped here for the same reason it is on the other list
// endpoints: the bound is stated once rather than per query.
//
// The cursor is decoded, validated and parsed here, for the reason every cursor in
// this feature is: a token is a string this build did not write, so it cannot be
// trusted to hold a usable position. Parsing the timestamp before the repository means
// an unusable token is rejected without running a query or taking a connection.
//
// This listing never triggers enrichment and never writes. Enrichment is scheduled by
// saving and by an explicit request; looking at a collection is not a reason to fetch
// anything. That is why the service holds no enqueuer here.
//
// The collection the page came from is returned alongside it, reusing the row the
// ownership check already read. It is present even when the page is empty, because an
// empty collection is still a collection a client asked about by name.
func (s *service) ListSavedItemsInCollection(
	ctx context.Context,
	userID uuid.UUID,
	collectionID uuid.UUID,
	limit int,
	cursor string,
) (ListSavedItemsInCollectionResult, error) {
	if limit <= 0 {
		limit = SavedItemListDefaultLimit
	}

	if limit > SavedItemListMaxLimit {
		limit = SavedItemListMaxLimit
	}

	params := ListSavedItemsInCollectionParams{
		UserID:       userID,
		CollectionID: collectionID,
		Limit:        limit,
	}

	// The cursor is settled before ownership is looked up. A token that cannot be used
	// is refused on its own terms, and there is no collection being shown for it to be
	// checked against, so spending a query on it would be answering a question the
	// request has already failed.
	if cursor != "" {
		decoded, err := decodeListSavedItemsInCollectionCursor(cursor)
		if err != nil {
			return ListSavedItemsInCollectionResult{}, err
		}

		params.Cursor = decoded
	}

	// Ownership next, so another user's collection never reaches an item query. The
	// error is the same one an unknown id produces, which is what keeps this endpoint
	// from disclosing whether a collection id exists.
	//
	// The row is kept rather than discarded. It already holds the id and name the
	// response reports, so carrying it costs nothing and avoids a second lookup for
	// data this call has just read.
	collectionRow, err := s.repository.GetCollectionByIDForUser(
		ctx,
		GetCollectionByIDForUserParams{
			ID:     collectionID,
			UserID: userID,
		},
	)
	if err != nil {
		return ListSavedItemsInCollectionResult{}, err
	}

	result, err := s.repository.ListSavedItemsInCollection(ctx, params)
	if err != nil {
		return ListSavedItemsInCollectionResult{}, err
	}

	result.Collection = collectionRow

	// The next cursor is derived from the last item actually returned, never the
	// lookahead row: a cursor built from a row the client never received would resume
	// past it and skip one item at every page boundary.
	if result.HasMore && len(result.SavedItems) > 0 {
		token, err := encodeListSavedItemsInCollectionCursor(result.SavedItems)
		if err != nil {
			return ListSavedItemsInCollectionResult{}, apperror.Internal(err)
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

// decodeListSavedItemsInCollectionCursor turns an opaque token into a validated
// cursor.
//
// Every failure is the same 400 with the same code, for the reason the collection
// listing does it that way: a cursor is not a capability, so "malformed" and
// "unusable" mean one thing to a caller, which is to start over without a cursor.
//
// There is no sort or group to check here. This listing has one ordering and no
// pinned row, so a token cannot be mismatched against anything and there is nothing
// for a sort comparison to reject.
func decodeListSavedItemsInCollectionCursor(
	token string,
) (*ListSavedItemsInCollectionCursor, error) {
	decoded, err := pagination.DecodeVersioned[ListSavedItemsInCollectionCursor](token)
	if err != nil {
		return nil, invalidCursorError(err)
	}

	// Both halves are required: an ordering on created_at alone is not total, so a
	// position without the tie-breaker could not describe where a page ended.
	if decoded.Value == nil || decoded.ID == nil {
		return nil, invalidCursorError(nil)
	}

	if *decoded.Value == "" || *decoded.ID == uuid.Nil {
		return nil, invalidCursorError(nil)
	}

	// The position is compared against a timestamptz column, so it has to actually be
	// a timestamp. Checking it here rather than in the repository turns an unusable
	// token into the 400 it is before any query runs.
	parsed, err := parseCursorTimestamp(*decoded.Value)
	if err != nil {
		return nil, invalidCursorError(err)
	}

	decoded.Timestamp = parsed

	return &decoded, nil
}

// encodeListSavedItemsInCollectionCursor builds the token for the page ending at the
// given item.
//
// The position is read from the item as stored, never re-derived, so the value in the
// token is the one the query ordered by.
func encodeListSavedItemsInCollectionCursor(
	savedItems []ListedSavedItem,
) (string, error) {
	last := savedItems[len(savedItems)-1]

	value := last.CreatedAt.Time.Format(time.RFC3339Nano)

	return pagination.Encode(ListSavedItemsInCollectionCursor{
		Version: pagination.CurrentVersion,
		Value:   &value,
		ID:      &last.ID,
	})
}

// DeleteCollection removes one of the user's collections, and deals with the saved
// items in it the way the caller asked for.
//
// The service owns the request rules here, exactly as it owns the name rules for
// putting an item into a collection: what the request is allowed to mean is decided
// before the database is touched, so a request that says nothing coherent about the
// user's saved items never reaches a statement that could act on it.
//
// Validation is not defensive tidying. Each of these rules exists because the
// alternative would be guessing:
//
//   - A missing or unrecognised action is rejected. The two actions are opposites
//     in their consequences, one of them destroys content, so choosing on the
//     caller's behalf is not an option.
//   - A target given with 'delete' is rejected rather than ignored, because it means
//     the caller believed the items were going somewhere.
//   - A target missing from 'move' is rejected, because 'move' with nowhere to move
//     to has no meaning.
//   - A target equal to the source is rejected, because moving items into the
//     collection being deleted is not a move.
//
// Unsorted is not special-cased here. It is not a permitted target by name, it is
// not a fallback when the requested target is missing, and it is not rejected as a
// target: it is an ordinary collection that happens to be the one this collection
// may not delete, which the repository decides by its system_key. That is why the
// request carries an id and never a name.
//
// The transaction, the lock ordering and the foreign key handling all belong to the
// repository, because splitting them across calls could leave a collection removed
// with its items already gone.
func (s *service) DeleteCollection(
	ctx context.Context,
	params DeleteCollectionParams,
) error {
	switch params.Action {
	case SavedItemsActionDelete:
		if params.TargetCollectionID != uuid.Nil {
			return apperror.BadRequestWith(
				CodeInvalidCollectionDeleteTarget,
				"target_collection_id must be null when saved_items_action is delete",
				nil,
			)
		}

	case SavedItemsActionMove:
		if params.TargetCollectionID == uuid.Nil {
			return apperror.BadRequestWith(
				CodeInvalidCollectionDeleteTarget,
				"target_collection_id is required when saved_items_action is move",
				nil,
			)
		}

		if params.TargetCollectionID == params.CollectionID {
			return apperror.BadRequestWith(
				CodeInvalidCollectionDeleteTarget,
				"target_collection_id must not be the collection being deleted",
				nil,
			)
		}

	default:
		return apperror.BadRequestWith(
			CodeInvalidCollectionDeleteAction,
			"saved_items_action must be delete or move",
			nil,
		)
	}

	return s.repository.DeleteCollection(ctx, params)
}

// normalizeCollectionName trims the submitted name and validates it.
//
// Surrounding whitespace is stripped so the stored display name and the
// collections_user_name_unique expression, which compares lower(btrim(name)),
// agree on what the name is. Trimming every kind of Unicode whitespace, rather
// than only the plain spaces btrim() removes, also keeps the stored name from
// looking different from the name the unique index matched.
//
// The name is not lowercased: it is the user's display name and is stored as
// submitted. Case-insensitive uniqueness is PostgreSQL's job, decided by the
// unique index, so no existence check is performed here. Deciding it in Go would
// mean two sources of truth that can disagree with the index.
func normalizeCollectionName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)

	if trimmed == "" {
		return "", apperror.BadRequestWith(
			CodeInvalidCollectionName,
			"collection name must not be blank",
			nil,
		)
	}

	// Measured in runes, so a multi-byte name is not rejected for being long in
	// bytes.
	if utf8.RuneCountInString(trimmed) > CollectionNameMaxLength {
		return "", apperror.BadRequestWith(
			CodeInvalidCollectionName,
			"collection name is too long",
			nil,
		)
	}

	return trimmed, nil
}
