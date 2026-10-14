package session

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

type SessionCreator interface {
	CreateSession(
		ctx context.Context,
		userID uuid.UUID,
		metadata SessionMetadata,
	) (CreateSessionResult, error)
}

type SessionService interface {
	RefreshToken(
		ctx context.Context,
		refreshToken string,
	) (RefreshTokenResult, error)

	Logout(
		ctx context.Context,
		userID uuid.UUID,
		sessionID uuid.UUID,
	) error

	ListSessions(
		ctx context.Context,
		userID uuid.UUID,
		page int,
		limit int,
	) (ListSessionsResult, error)

	RevokeSessionForUser(
		ctx context.Context,
		userID uuid.UUID,
		sessionID uuid.UUID,
	) error

	// CleanupSessions deletes the sessions that can no longer be used. It is the
	// maintenance surface, not a user-facing one: no user credential reaches it
	// and the caller is an external scheduler.
	CleanupSessions(ctx context.Context) (CleanupSessionsResult, error)
}

type service struct {
	repository           Repository
	accessTokenGenerator *security.AccessTokenGenerator
	log                  *zap.Logger
}

func NewService(
	repository Repository,
	accessTokenGenerator *security.AccessTokenGenerator,
	log *zap.Logger,
) *service {
	return &service{
		repository:           repository,
		accessTokenGenerator: accessTokenGenerator,
		log:                  log,
	}
}

type CreateSessionResult struct {
	Session      sessiondb.Session
	RefreshToken string
}

func (s *service) CreateSession(
	ctx context.Context,
	userID uuid.UUID,
	metadata SessionMetadata,
) (CreateSessionResult, error) {
	refreshToken, err := security.GenerateToken()
	if err != nil {
		return CreateSessionResult{}, apperror.Internal(err)
	}

	refreshTokenHash := security.HashToken(refreshToken)

	sessionRecord, _, err := s.repository.CreateSession(
		ctx,
		sessiondb.CreateSessionParams{
			UserID:         userID,
			Platform:       metadata.Platform,
			InstallationID: metadata.InstallationID,
			DeviceName:     metadata.DeviceName,
			UserAgent:      metadata.UserAgent,
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  time.Now().Add(SessionAbsoluteLifetime),
				Valid: true,
			},
		},
		refreshTokenHash,
	)
	if err != nil {
		return CreateSessionResult{}, err
	}

	return CreateSessionResult{
		Session:      sessionRecord,
		RefreshToken: refreshToken,
	}, nil
}

type RefreshTokenResult struct {
	AccessToken  string
	RefreshToken string
}

func (s *service) RefreshToken(
	ctx context.Context,
	refreshToken string,
) (RefreshTokenResult, error) {
	newRefreshToken, err := security.GenerateToken()
	if err != nil {
		return RefreshTokenResult{}, apperror.Internal(err)
	}

	newRefreshTokenHash := security.HashToken(newRefreshToken)
	refreshTokenHash := security.HashToken(refreshToken)

	currentToken, _, err := s.repository.RefreshToken(
		ctx,
		refreshTokenHash,
		newRefreshTokenHash,
		RefreshTokenPolicy{
			IdleLifetime: SessionIdleLifetime,
		},
	)
	if err != nil {
		return RefreshTokenResult{}, err
	}

	accessToken, err := s.accessTokenGenerator.Generate(
		currentToken.UserID,
		currentToken.SessionID,
		AccessTokenLifetime,
	)
	if err != nil {
		return RefreshTokenResult{}, apperror.Internal(err)
	}

	return RefreshTokenResult{
		AccessToken:  accessToken,
		RefreshToken: newRefreshToken,
	}, nil
}

// Logout ends the caller's own session.
//
// The user id is carried alongside the session id rather than looked up from it.
// Both come from the verified access token, so neither is caller-supplied, and
// the repository matches both: a session id that belongs to somebody else
// matches nothing and leaves that session untouched.
//
// It stays idempotent. Logging out twice, or logging out of a session that has
// already been revoked, is a success, because the caller's intent — that they
// hold no usable session — already holds.
func (s *service) Logout(
	ctx context.Context,
	userID uuid.UUID,
	sessionID uuid.UUID,
) error {
	return s.repository.RevokeSession(ctx, sessionID, userID)
}

type ListSessionsResult struct {
	Sessions   []sessiondb.Session
	Page       int
	Limit      int
	Total      int64
	TotalPages int
}

func (s *service) ListSessions(
	ctx context.Context,
	userID uuid.UUID,
	page int,
	limit int,
) (ListSessionsResult, error) {
	if page < 1 {
		page = 1
	}

	if limit <= 0 {
		limit = SessionListDefaultLimit
	}

	if limit > SessionListMaxLimit {
		limit = SessionListMaxLimit
	}

	offset := int32((page - 1) * limit)

	sessions, err := s.repository.ListSessions(
		ctx,
		userID,
		offset,
		int32(limit),
	)
	if err != nil {
		return ListSessionsResult{}, err
	}

	total, err := s.repository.CountSessions(ctx, userID)
	if err != nil {
		return ListSessionsResult{}, err
	}

	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(limit) - 1) / int64(limit))
	}

	return ListSessionsResult{
		Sessions:   sessions,
		Page:       page,
		Limit:      limit,
		Total:      total,
		TotalPages: totalPages,
	}, nil
}

func (s *service) RevokeSessionForUser(
	ctx context.Context,
	userID uuid.UUID,
	sessionID uuid.UUID,
) error {
	return s.repository.RevokeSessionForUser(
		ctx,
		sessionID,
		userID,
	)
}

// CleanupSessionsResult reports what one execution did.
type CleanupSessionsResult struct {
	// DeletedSessions is the total across every batch in this execution. The
	// response reports this and nothing else; the batch figures below exist for
	// the log.
	DeletedSessions int64

	// Batches is how many statements completed successfully.
	Batches int

	// BacklogCapped reports that the execution stopped because it reached
	// SessionCleanupMaxBatches with a full final batch, rather than because
	// there was nothing left to delete.
	//
	// It is deliberately not a backlog size. A full batch means "there may be
	// more", never "there are exactly this many left", and measuring the rest
	// would mean a second scan of the same predicate purely to produce a number
	// nothing acts on. The next execution reports its own result.
	BacklogCapped bool
}

// CleanupSessions deletes sessions that can no longer be used, in bounded
// batches, until there is nothing left to delete or the batch cap is reached.
//
// The loop lives here rather than in the repository because it is orchestration:
// each iteration is an independent statement with its own transaction, and the
// repository's job is to execute one of them correctly.
//
// Each batch commits on its own. That is why the failure log reports the rows
// already deleted rather than zero — those deletions are durable, and reporting
// zero would describe durable state that did not happen.
//
// It is safe to run concurrently with itself and safe to run repeatedly. Two
// invocations take different batches rather than blocking, because the
// statement skips rows another transaction already holds, and a repeated
// invocation simply finds less to do. Neither needs a lock held across
// executions, because neither can remove a session the refresh path would still
// accept: every row matched here is one refresh already refuses.
func (s *service) CleanupSessions(
	ctx context.Context,
) (CleanupSessionsResult, error) {
	started := time.Now()

	var (
		deleted int64
		batches int
		capped  bool
	)

	for batches < SessionCleanupMaxBatches {
		removed, err := s.repository.DeleteCleanupEligibleSessions(
			ctx,
			SessionIdleLifetime,
			SessionCleanupBatchSize,
		)
		if err != nil {
			// backlog_capped is false here and always will be: the loop only
			// reaches its cap by completing every batch, so an execution that
			// ended in an error stopped for that reason and not this one.
			s.log.Error(
				"session cleanup failed",
				zap.Int64("deleted_sessions", deleted),
				zap.Int("batches_completed", batches),
				zap.Duration("duration", time.Since(started)),
				zap.Bool("backlog_capped", capped),
				zap.Error(err),
			)

			return CleanupSessionsResult{}, err
		}

		deleted += removed
		batches++

		// A batch short of the limit means the eligible set is exhausted. Stop
		// here rather than running one more statement that would delete nothing.
		if removed < int64(SessionCleanupBatchSize) {
			break
		}
	}

	capped = batches == SessionCleanupMaxBatches

	// Logged on success too, including when it deleted nothing. A scheduler that
	// silently stopped calling would otherwise be indistinguishable from a table
	// that happened to be clean, and the next person to look would have no way
	// to tell which happened.
	s.log.Info(
		"session cleanup completed",
		zap.Int64("deleted_sessions", deleted),
		zap.Int("batches", batches),
		zap.Int("batch_size", SessionCleanupBatchSize),
		zap.Duration("duration", time.Since(started)),
		zap.Bool("backlog_capped", capped),
	)

	return CleanupSessionsResult{
		DeletedSessions: deleted,
		Batches:         batches,
		BacklogCapped:   capped,
	}, nil
}
