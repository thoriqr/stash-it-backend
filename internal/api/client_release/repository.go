package client_release

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	clientreleasedb "github.com/thoriqr/stash-it-backend/internal/api/client_release/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

type repository struct {
	queries *clientreleasedb.Queries

	// pool is set only by NewAdminRepository. The read path never opens a
	// transaction, so its constructor leaves this nil.
	pool *pgxpool.Pool
}

func NewRepository(queries *clientreleasedb.Queries) Repository {
	return &repository{
		queries: queries,
	}
}

// GetChannel returns the published policy for a platform, or nil when the
// platform has no channel yet.
//
// This is a deliberate exception to the repository convention that translates
// pgx.ErrNoRows into apperror.NotFound: a missing channel is an ordinary state
// the endpoint reports as status none, not a missing resource. Turning it into a
// 404 would force the handler to convert an error back into a successful answer.
func (r *repository) GetChannel(
	ctx context.Context,
	platform string,
) (*Channel, error) {
	row, err := r.queries.GetClientReleaseChannel(ctx, platform)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}

		return nil, apperror.Internal(err)
	}

	return &Channel{
		Platform: row.Platform,
		Latest: Release{
			Version:       row.LatestVersion,
			Build:         int64(row.LatestBuild),
			DownloadURL:   row.DownloadUrl,
			SHA256:        row.Sha256,
			FileSizeBytes: row.FileSizeBytes,
			ReleaseNotes:  row.ReleaseNotes,
			PublishedAt:   row.PublishedAt.Time,
		},
		MinSupported: ReleaseRef{
			Version: row.MinSupportedVersion,
			Build:   int64(row.MinSupportedBuild),
		},
	}, nil
}
