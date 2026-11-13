package client_release

import (
	"context"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// Service answers a client's update check.
type Service interface {
	Check(ctx context.Context, platform, build string) (CheckResult, error)
}

type service struct {
	repository Repository
	log        *zap.Logger
}

func NewService(repository Repository, log *zap.Logger) *service {
	return &service{
		repository: repository,
		log:        log,
	}
}

// CheckResult is the decision for one client.
//
// Latest and MinSupported are nil only when the platform has no channel yet.
// When a channel exists, both are present even if Status is none: the response
// then still reports which release is current.
type CheckResult struct {
	Platform     string
	Status       string
	CurrentBuild int64
	MinSupported *ReleaseRef
	Latest       *Release
}

// Check validates the caller's platform and build, reads the platform's channel
// and derives the decision.
//
// The comparison is on build integers only. Version strings are display values
// and are never ordered: "1.10.0" sorts below "1.9.0" lexicographically, so a
// string comparison would offer a downgrade.
func (s *service) Check(
	ctx context.Context,
	platform string,
	rawBuild string,
) (CheckResult, error) {
	normalizedPlatform, err := normalizePlatform(platform)
	if err != nil {
		return CheckResult{}, err
	}

	currentBuild, err := parseBuild(rawBuild)
	if err != nil {
		return CheckResult{}, err
	}

	channel, err := s.repository.GetChannel(ctx, normalizedPlatform)
	if err != nil {
		return CheckResult{}, err
	}

	result := CheckResult{
		Platform:     normalizedPlatform,
		Status:       StatusNone,
		CurrentBuild: currentBuild,
	}

	// No channel is the state of a deployment that has not published anything for
	// this platform yet. It is not an error and it is not a 404: the client has no
	// action to take, which is exactly what status none means.
	if channel == nil {
		return result, nil
	}

	minimum := channel.MinSupported
	latest := channel.Latest

	// A minimum above the latest release cannot be satisfied by anything offered.
	// Clamping it to the latest keeps the response coherent, and logging makes the
	// misconfiguration visible without turning a client's update check into a 500.
	if minimum.Build > latest.Build {
		s.log.Error(
			"client release channel has a minimum above its latest release",
			zap.String("platform", normalizedPlatform),
			zap.Int64("min_supported_build", minimum.Build),
			zap.Int64("latest_build", latest.Build),
		)

		minimum = ReleaseRef{
			Version: latest.Version,
			Build:   latest.Build,
		}
	}

	result.MinSupported = &minimum
	result.Latest = &latest

	switch {
	case currentBuild < minimum.Build:
		result.Status = StatusRequired
	case currentBuild < latest.Build:
		result.Status = StatusOptional
	default:
		result.Status = StatusNone
	}

	return result, nil
}

func normalizePlatform(raw string) (string, error) {
	platform := strings.ToLower(strings.TrimSpace(raw))

	switch platform {
	case PlatformAndroid, PlatformWindows:
		return platform, nil
	default:
		return "", apperror.BadRequestWith(
			CodeInvalidPlatform,
			"platform must be android or windows",
			nil,
		)
	}
}

func parseBuild(raw string) (int64, error) {
	trimmed := strings.TrimSpace(raw)

	build, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || build <= 0 {
		// The parse error is deliberately not retained: it contains the caller's
		// input, and neither the response nor the log needs it.
		return 0, apperror.BadRequestWith(
			CodeInvalidClientBuild,
			"build must be a positive integer",
			nil,
		)
	}

	return build, nil
}
