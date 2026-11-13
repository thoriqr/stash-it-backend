package client_release

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	clientreleasedb "github.com/thoriqr/stash-it-backend/internal/api/client_release/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// NewAdminRepository builds the maintenance repository over the same query set as
// the public check, with the pool the transactions need.
func NewAdminRepository(
	queries *clientreleasedb.Queries,
	pool *pgxpool.Pool,
) AdminRepository {
	return &repository{
		queries: queries,
		pool:    pool,
	}
}

// PublishRelease records an immutable release and moves the platform's latest
// pointer to it.
//
// The channel row is read FOR UPDATE so the rules are decided against the state
// that is being changed, not against a snapshot taken before the transaction. A
// publish only moves forward: an older build is refused rather than silently
// becoming latest. When force is set, the same release also becomes the minimum.
func (r *repository) PublishRelease(
	ctx context.Context,
	params PublishReleaseParams,
) (PublishReleaseResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return PublishReleaseResult{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	inserted, err := qtx.InsertClientRelease(
		ctx,
		clientreleasedb.InsertClientReleaseParams{
			Platform:      params.Platform,
			Version:       params.Version,
			Build:         int32(params.Build),
			DownloadUrl:   params.DownloadURL,
			Sha256:        params.SHA256,
			FileSizeBytes: params.FileSizeBytes,
			ReleaseNotes:  params.ReleaseNotes,
			GitTag:        params.GitTag,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublishReleaseResult{}, apperror.ConflictWith(
				CodeClientReleaseAlreadyExists,
				"client release already exists",
				nil,
			)
		}

		return PublishReleaseResult{}, apperror.Internal(err)
	}

	channel, err := qtx.GetClientReleaseChannelForUpdate(
		ctx,
		params.Platform,
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// The first release for this platform is both the latest and the minimum.
		if err := qtx.InsertClientReleaseChannel(
			ctx,
			clientreleasedb.InsertClientReleaseChannelParams{
				Platform:              params.Platform,
				LatestReleaseID:       inserted.ID,
				MinSupportedReleaseID: inserted.ID,
			},
		); err != nil {
			return PublishReleaseResult{}, apperror.Internal(err)
		}

	case err != nil:
		return PublishReleaseResult{}, apperror.Internal(err)

	default:
		if params.Build <= int64(channel.LatestBuild) {
			return PublishReleaseResult{}, apperror.ConflictWith(
				CodeInvalidReleasePolicy,
				"a new release must have a build above the current latest",
				nil,
			)
		}

		minSupportedID := channel.MinSupportedReleaseID

		if params.Force {
			minSupportedID = inserted.ID
		} else if int64(channel.MinSupportedBuild) > params.Build {
			// A channel whose minimum is above its latest is already corrupt.
			// Publishing without force cannot repair it, so it is refused rather
			// than written into another invalid state.
			return PublishReleaseResult{}, apperror.ConflictWith(
				CodeInvalidReleasePolicy,
				"the minimum supported release is above the new release",
				nil,
			)
		}

		if err := qtx.UpdateClientReleaseChannelPolicy(
			ctx,
			clientreleasedb.UpdateClientReleaseChannelPolicyParams{
				LatestReleaseID:       inserted.ID,
				MinSupportedReleaseID: minSupportedID,
				Platform:              params.Platform,
			},
		); err != nil {
			return PublishReleaseResult{}, apperror.Internal(err)
		}
	}

	updated, err := qtx.GetClientReleaseChannel(ctx, params.Platform)
	if err != nil {
		return PublishReleaseResult{}, apperror.Internal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return PublishReleaseResult{}, apperror.Internal(err)
	}

	return PublishReleaseResult{
		Release: releaseFromInsertRow(inserted),
		Policy:  policyFromChannelRow(updated),
		Forced:  params.Force,
	}, nil
}

// SetMinSupported forces the platform's minimum to an existing release.
//
// The target must not be above the latest release: forcing to something nobody is
// offered would be an instruction with no artifact behind it.
func (r *repository) SetMinSupported(
	ctx context.Context,
	params ReleaseActionParams,
) (ChannelPolicy, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ChannelPolicy{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	channel, err := qtx.GetClientReleaseChannelForUpdate(
		ctx,
		params.Platform,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelPolicy{}, releaseNotFound()
		}

		return ChannelPolicy{}, apperror.Internal(err)
	}

	target, err := qtx.GetClientReleaseByPlatformAndBuild(
		ctx,
		clientreleasedb.GetClientReleaseByPlatformAndBuildParams{
			Platform: params.Platform,
			Build:    int32(params.Build),
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelPolicy{}, releaseNotFound()
		}

		return ChannelPolicy{}, apperror.Internal(err)
	}

	if target.Build > channel.LatestBuild {
		return ChannelPolicy{}, apperror.ConflictWith(
			CodeInvalidReleasePolicy,
			"the minimum supported release cannot be above the latest release",
			nil,
		)
	}

	if err := qtx.UpdateClientReleaseChannelPolicy(
		ctx,
		clientreleasedb.UpdateClientReleaseChannelPolicyParams{
			LatestReleaseID:       channel.LatestReleaseID,
			MinSupportedReleaseID: target.ID,
			Platform:              params.Platform,
		},
	); err != nil {
		return ChannelPolicy{}, apperror.Internal(err)
	}

	updated, err := qtx.GetClientReleaseChannel(ctx, params.Platform)
	if err != nil {
		return ChannelPolicy{}, apperror.Internal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ChannelPolicy{}, apperror.Internal(err)
	}

	return policyFromChannelRow(updated), nil
}

// Rollback moves the latest pointer back to an existing release.
//
// The minimum is lowered only when it would otherwise sit above the new latest.
// Keeping it where it is means a rollback does not newly force clients that were
// already allowed to stay on an older build.
func (r *repository) Rollback(
	ctx context.Context,
	params ReleaseActionParams,
) (ChannelPolicy, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ChannelPolicy{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	channel, err := qtx.GetClientReleaseChannelForUpdate(
		ctx,
		params.Platform,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelPolicy{}, releaseNotFound()
		}

		return ChannelPolicy{}, apperror.Internal(err)
	}

	target, err := qtx.GetClientReleaseByPlatformAndBuild(
		ctx,
		clientreleasedb.GetClientReleaseByPlatformAndBuildParams{
			Platform: params.Platform,
			Build:    int32(params.Build),
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelPolicy{}, releaseNotFound()
		}

		return ChannelPolicy{}, apperror.Internal(err)
	}

	if target.Build > channel.LatestBuild {
		return ChannelPolicy{}, apperror.ConflictWith(
			CodeInvalidReleasePolicy,
			"a rollback target cannot be above the current latest release",
			nil,
		)
	}

	minSupportedID := channel.MinSupportedReleaseID

	if channel.MinSupportedBuild > target.Build {
		minSupportedID = target.ID
	}

	if err := qtx.UpdateClientReleaseChannelPolicy(
		ctx,
		clientreleasedb.UpdateClientReleaseChannelPolicyParams{
			LatestReleaseID:       target.ID,
			MinSupportedReleaseID: minSupportedID,
			Platform:              params.Platform,
		},
	); err != nil {
		return ChannelPolicy{}, apperror.Internal(err)
	}

	updated, err := qtx.GetClientReleaseChannel(ctx, params.Platform)
	if err != nil {
		return ChannelPolicy{}, apperror.Internal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ChannelPolicy{}, apperror.Internal(err)
	}

	return policyFromChannelRow(updated), nil
}

// ListReleases returns a platform's releases newest first and its current policy.
func (r *repository) ListReleases(
	ctx context.Context,
	platform string,
) (ListReleasesResult, error) {
	rows, err := r.queries.ListClientReleases(ctx, platform)
	if err != nil {
		return ListReleasesResult{}, apperror.Internal(err)
	}

	releases := make([]Release, 0, len(rows))

	for _, row := range rows {
		releases = append(releases, releaseFromListRow(row))
	}

	channel, err := r.queries.GetClientReleaseChannel(ctx, platform)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ListReleasesResult{
				Releases: releases,
			}, nil
		}

		return ListReleasesResult{}, apperror.Internal(err)
	}

	policy := policyFromChannelRow(channel)

	return ListReleasesResult{
		Releases: releases,
		Policy:   &policy,
	}, nil
}

func releaseNotFound() *apperror.AppError {
	return apperror.NotFoundWith(
		CodeReleaseNotFound,
		"client release not found",
		nil,
	)
}

func releaseFromInsertRow(
	row clientreleasedb.InsertClientReleaseRow,
) Release {
	return Release{
		Version:       row.Version,
		Build:         int64(row.Build),
		DownloadURL:   row.DownloadUrl,
		SHA256:        row.Sha256,
		FileSizeBytes: row.FileSizeBytes,
		ReleaseNotes:  row.ReleaseNotes,
		GitTag:        row.GitTag,
		PublishedAt:   row.PublishedAt.Time,
	}
}

func releaseFromListRow(
	row clientreleasedb.ListClientReleasesRow,
) Release {
	return Release{
		Version:       row.Version,
		Build:         int64(row.Build),
		DownloadURL:   row.DownloadUrl,
		SHA256:        row.Sha256,
		FileSizeBytes: row.FileSizeBytes,
		ReleaseNotes:  row.ReleaseNotes,
		GitTag:        row.GitTag,
		PublishedAt:   row.PublishedAt.Time,
	}
}

func policyFromChannelRow(
	row clientreleasedb.GetClientReleaseChannelRow,
) ChannelPolicy {
	return ChannelPolicy{
		Latest: ReleaseRef{
			Version: row.LatestVersion,
			Build:   int64(row.LatestBuild),
		},
		MinSupported: ReleaseRef{
			Version: row.MinSupportedVersion,
			Build:   int64(row.MinSupportedBuild),
		},
	}
}
