package client_release

import (
	"context"
	"regexp"
	"strings"

	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// maxReleaseBuild is the largest value the INTEGER build column accepts. The
// public check may receive a larger build from a client, but a release row cannot
// be stored with one.
const maxReleaseBuild = int64(1<<31 - 1)

var (
	semverPattern = regexp.MustCompile(
		`^(0|[1-9][0-9]*)[.](0|[1-9][0-9]*)[.](0|[1-9][0-9]*)$`,
	)
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// AdminService is the maintenance side of client releases.
type AdminService interface {
	PublishRelease(
		ctx context.Context,
		request PublishReleaseRequest,
	) (PublishReleaseResult, error)

	ForceRelease(
		ctx context.Context,
		request ReleaseActionRequest,
	) (ChannelPolicy, error)

	RollbackRelease(
		ctx context.Context,
		request ReleaseActionRequest,
	) (ChannelPolicy, error)

	ListReleases(
		ctx context.Context,
		platform string,
	) (ListReleasesResult, error)
}

type adminService struct {
	repository AdminRepository
	log        *zap.Logger
}

func NewAdminService(
	repository AdminRepository,
	log *zap.Logger,
) *adminService {
	return &adminService{
		repository: repository,
		log:        log,
	}
}

// PublishRelease validates the artifact metadata and records it.
//
// The state rules — the new build must be above the current latest, and forcing
// moves the minimum — are enforced in the repository inside the transaction that
// holds the channel lock. This method validates only what can be decided from the
// request alone.
func (s *adminService) PublishRelease(
	ctx context.Context,
	request PublishReleaseRequest,
) (PublishReleaseResult, error) {
	params, err := validatePublishRelease(request)
	if err != nil {
		return PublishReleaseResult{}, err
	}

	result, err := s.repository.PublishRelease(ctx, params)
	if err != nil {
		return PublishReleaseResult{}, err
	}

	s.log.Info(
		"client release published",
		zap.String("platform", params.Platform),
		zap.Int64("build", params.Build),
		zap.Bool("forced", params.Force),
	)

	return result, nil
}

func (s *adminService) ForceRelease(
	ctx context.Context,
	request ReleaseActionRequest,
) (ChannelPolicy, error) {
	params, err := validateReleaseAction(request)
	if err != nil {
		return ChannelPolicy{}, err
	}

	policy, err := s.repository.SetMinSupported(ctx, params)
	if err != nil {
		return ChannelPolicy{}, err
	}

	s.log.Info(
		"client release minimum updated",
		zap.String("platform", params.Platform),
		zap.Int64("build", params.Build),
	)

	return policy, nil
}

func (s *adminService) RollbackRelease(
	ctx context.Context,
	request ReleaseActionRequest,
) (ChannelPolicy, error) {
	params, err := validateReleaseAction(request)
	if err != nil {
		return ChannelPolicy{}, err
	}

	policy, err := s.repository.Rollback(ctx, params)
	if err != nil {
		return ChannelPolicy{}, err
	}

	s.log.Info(
		"client release rolled back",
		zap.String("platform", params.Platform),
		zap.Int64("build", params.Build),
	)

	return policy, nil
}

func (s *adminService) ListReleases(
	ctx context.Context,
	platform string,
) (ListReleasesResult, error) {
	normalized, err := normalizePlatform(platform)
	if err != nil {
		return ListReleasesResult{}, err
	}

	return s.repository.ListReleases(ctx, normalized)
}

func validatePublishRelease(
	request PublishReleaseRequest,
) (PublishReleaseParams, error) {
	platform, err := normalizePlatform(request.Platform)
	if err != nil {
		return PublishReleaseParams{}, err
	}

	if !semverPattern.MatchString(request.Version) {
		return PublishReleaseParams{}, invalidReleaseMetadata(
			"version must be MAJOR.MINOR.PATCH",
		)
	}

	if request.Build <= 0 || request.Build > maxReleaseBuild {
		return PublishReleaseParams{}, invalidReleaseMetadata(
			"build must be a positive 32-bit integer",
		)
	}

	if !strings.HasPrefix(request.DownloadURL, "https://") {
		return PublishReleaseParams{}, invalidReleaseMetadata(
			"download_url must use https",
		)
	}

	if !sha256Pattern.MatchString(request.SHA256) {
		return PublishReleaseParams{}, invalidReleaseMetadata(
			"sha256 must be 64 lowercase hex characters",
		)
	}

	if request.FileSizeBytes <= 0 {
		return PublishReleaseParams{}, invalidReleaseMetadata(
			"file_size_bytes must be positive",
		)
	}

	return PublishReleaseParams{
		Platform:      platform,
		Version:       request.Version,
		Build:         request.Build,
		DownloadURL:   request.DownloadURL,
		SHA256:        request.SHA256,
		FileSizeBytes: request.FileSizeBytes,
		ReleaseNotes:  request.ReleaseNotes,
		GitTag:        request.GitTag,
		Force:         request.Force,
	}, nil
}

func validateReleaseAction(
	request ReleaseActionRequest,
) (ReleaseActionParams, error) {
	platform, err := normalizePlatform(request.Platform)
	if err != nil {
		return ReleaseActionParams{}, err
	}

	if request.Build <= 0 || request.Build > maxReleaseBuild {
		return ReleaseActionParams{}, invalidReleaseMetadata(
			"build must be a positive 32-bit integer",
		)
	}

	return ReleaseActionParams{
		Platform: platform,
		Build:    request.Build,
	}, nil
}

func invalidReleaseMetadata(message string) *apperror.AppError {
	return apperror.BadRequestWith(
		CodeInvalidReleaseMetadata,
		message,
		nil,
	)
}
