package saved_item

import (
	"context"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	saveditemdb "github.com/thoriqr/stash-it-backend/internal/api/saved_item/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
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
	) error

	List(
		ctx context.Context,
		userID uuid.UUID,
		page int,
		limit int,
	) (ListResult, error)
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

type CreateResult struct {
	SavedItem saveditemdb.SavedItem
}

func (s *service) Create(
	ctx context.Context,
	userID uuid.UUID,
	rawURL string,
) (CreateResult, error) {
	domain, err := deriveDomain(rawURL)
	if err != nil {
		return CreateResult{}, err
	}

	// platform here is the content/source platform (youtube, tiktok, instagram,
	// ...). It is NOT the client platform that sessions.platform records from
	// X-Platform, so no client header is read. It stays NULL in Phase A:
	// detection belongs to the future metadata/enrichment phase, which runs after
	// the save and must never block it.
	//
	// title is likewise not client supplied and stays NULL until background
	// metadata extraction populates it.
	savedItem, err := s.repository.CreateSavedItem(
		ctx,
		saveditemdb.CreateSavedItemParams{
			UserID:   userID,
			Url:      rawURL,
			Domain:   optionalText(domain),
			Platform: pgtype.Text{},
			Title:    pgtype.Text{},
		},
	)
	if err != nil {
		return CreateResult{}, err
	}

	return CreateResult{
		SavedItem: savedItem,
	}, nil
}

type GetResult struct {
	SavedItem saveditemdb.SavedItem
}

func (s *service) Get(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) (GetResult, error) {
	savedItem, err := s.repository.GetSavedItemByIDForUser(
		ctx,
		userID,
		savedItemID,
	)
	if err != nil {
		return GetResult{}, err
	}

	return GetResult{
		SavedItem: savedItem,
	}, nil
}

func (s *service) Delete(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) error {
	return s.repository.DeleteSavedItemByIDForUser(
		ctx,
		userID,
		savedItemID,
	)
}

type ListResult struct {
	SavedItems []saveditemdb.SavedItem
	Page       int
	Limit      int
	Total      int64
	TotalPages int
}

func (s *service) List(
	ctx context.Context,
	userID uuid.UUID,
	page int,
	limit int,
) (ListResult, error) {
	if page < 1 {
		page = 1
	}

	if limit <= 0 {
		limit = SavedItemListDefaultLimit
	}

	if limit > SavedItemListMaxLimit {
		limit = SavedItemListMaxLimit
	}

	offset := int32((page - 1) * limit)

	savedItems, err := s.repository.ListSavedItems(
		ctx,
		userID,
		offset,
		int32(limit),
	)
	if err != nil {
		return ListResult{}, err
	}

	total, err := s.repository.CountSavedItems(ctx, userID)
	if err != nil {
		return ListResult{}, err
	}

	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(limit) - 1) / int64(limit))
	}

	return ListResult{
		SavedItems: savedItems,
		Page:       page,
		Limit:      limit,
		Total:      total,
		TotalPages: totalPages,
	}, nil
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
