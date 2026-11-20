-- Feedback is what a user tells the operator about the product: a bug they hit,
-- an idea they have, or anything else. It is a record of what they said, and
-- nothing in the product reads it.
--
-- One row is one message, written once by its owner and never edited by them.
-- The operator changes only its status, or removes the row.

CREATE TABLE feedback (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    user_id UUID NOT NULL
        REFERENCES users(id) ON DELETE CASCADE,

    type TEXT NOT NULL,

    -- message is required, and NOT NULL says so: it is the report. It is not
    -- validated here. A blank or over-long value is rejected as input by the
    -- request validator and again by the service, the same way saved_items.note's
    -- blank and length rules are. 000031 is the precedent for that split alone —
    -- note is an optional column, and this one is not.
    message TEXT NOT NULL,

    status TEXT NOT NULL DEFAULT 'new',

    -- Context recorded from the client that sent the report.
    --
    -- All three are nullable, and none carries an allowed-value constraint.
    -- Unlike client_releases.platform, which must stay exact because a channel
    -- points at those rows, a feedback row is a label on a report: a platform
    -- added later (ios, macos) must not require a migration here before anybody
    -- using it can report anything, and an unrecognised value must not cost a
    -- report that is otherwise worth keeping.
    --
    -- app_version and app_build are separate for the same reason client_releases
    -- keeps them apart: build is the integer a client compares, version is the
    -- string a person reads. Both come from the installed package, so they are
    -- always tied to the build that produced the report.
    platform    TEXT,
    app_version TEXT,
    app_build   INTEGER,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- type and status are plain TEXT plus a CHECK, never a PostgreSQL ENUM, so
    -- adding a value later is an ordinary constraint change. These two stay in
    -- the schema, unlike the input bounds above, because they are closed
    -- vocabularies the system owns: type is chosen from a fixed set, and status
    -- is set only by the operator. Nothing outside this table can legitimately
    -- introduce a new one.
    CONSTRAINT feedback_type_check
        CHECK (type IN ('bug', 'idea', 'other')),

    -- new -> planned -> closed, and new -> closed directly when there is nothing
    -- to do. There is no shipped or rejected: both end in closed, because nothing
    -- the operator does afterwards differs between them.
    CONSTRAINT feedback_status_check
        CHECK (status IN ('new', 'planned', 'closed'))
);

-- The operator reads newest first, optionally filtered by status. id DESC is the
-- tie-breaker and not decoration: created_at can repeat, and under LIMIT/OFFSET a
-- tied group split across a page boundary shows one row twice and skips another.
CREATE INDEX feedback_created_at_idx
    ON feedback (created_at DESC, id DESC);

CREATE INDEX feedback_status_created_at_idx
    ON feedback (status, created_at DESC, id DESC);

-- updated_at is how the operator tells a report they have already handled from
-- one they have not. Status is the only column that can change, so this is in
-- practice the moment it was last triaged.
CREATE TRIGGER feedback_set_updated_at
BEFORE UPDATE ON feedback
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();
