package feedback

import (
	"strings"

	"github.com/google/uuid"

	feedbackdb "github.com/thoriqr/stash-it-backend/internal/api/feedback/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// NormalizeContext trims and lowercases the client-supplied context, and enforces
// the length bounds.
//
// Lowercasing is not cosmetic. The platform arrives from whichever client is
// running, and without one canonical spelling "macOS", "MacOS" and "macos" would be
// three values that mean one thing, so grouping by platform would be unreliable
// exactly when it starts to matter. Normalization rewrites rather than refuses,
// which is the difference between it and a CHECK: a value this deployment does not
// recognize is still recorded, it is just recorded consistently.
//
// A field that is blank after trimming is absent rather than empty. Storing "" would
// make "the client said nothing" indistinguishable from "the client said something
// empty", and only the first of those is true.
//
// An over-long value is an error rather than a truncation. A client that sends a
// 300-character version string is a client bug, and storing a shortened one would
// hide it while recording a value nobody can match against a release.
func NormalizeContext(supplied Context) (Context, error) {
	platform, err := normalizeContextField(
		supplied.Platform,
		PlatformMaxBytes,
		"platform",
		true,
	)
	if err != nil {
		return Context{}, err
	}

	appVersion, err := normalizeContextField(
		supplied.AppVersion,
		AppVersionMaxBytes,
		"app_version",
		false,
	)
	if err != nil {
		return Context{}, err
	}

	var appBuild *int32

	if supplied.AppBuild != nil {
		if *supplied.AppBuild < 1 {
			return Context{}, apperror.Validation(
				[]apperror.ErrorField{{
					Field:   "app_build",
					Message: "is too small",
				}},
				nil,
			)
		}

		build := *supplied.AppBuild
		appBuild = &build
	}

	return Context{
		Platform:   platform,
		AppVersion: appVersion,
		AppBuild:   appBuild,
	}, nil
}

// normalizeContextField trims one context value, treats a blank one as absent,
// optionally lowercases it, and refuses an over-long one.
//
// lowercase is true only for platform, which is an identifier this deployment groups
// by. A version is left as the client wrote it because it is only ever compared
// against a release record, and changing its case would make it stop matching.
func normalizeContextField(
	value *string,
	maxBytes int,
	field string,
	lowercase bool,
) (*string, error) {
	if value == nil {
		return nil, nil
	}

	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}

	if lowercase {
		trimmed = strings.ToLower(trimmed)
	}

	if len(trimmed) > maxBytes {
		return nil, apperror.Validation(
			[]apperror.ErrorField{{
				Field:   field,
				Message: "is too large",
			}},
			nil,
		)
	}

	return &trimmed, nil
}

// newCreateFeedbackParams maps one accepted report onto the insert's parameters.
//
// The context reaches this already normalized, so an absent field becomes an unset
// pgtype value rather than an empty string. That is what the column stores as NULL.
func newCreateFeedbackParams(
	userID uuid.UUID,
	reportType string,
	message string,
	context Context,
) feedbackdb.CreateFeedbackParams {
	return feedbackdb.CreateFeedbackParams{
		UserID:     userID,
		Type:       reportType,
		Message:    message,
		Platform:   optionalText(context.Platform),
		AppVersion: optionalText(context.AppVersion),
		AppBuild:   optionalInt4(context.AppBuild),
	}
}
