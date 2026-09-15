CREATE TABLE sessions (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    user_id UUID NOT NULL
        REFERENCES users(id) ON DELETE CASCADE,

    platform TEXT NOT NULL,
    installation_id UUID,
    device_name TEXT,
    user_agent TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ
);

CREATE INDEX sessions_user_idx
    ON sessions (user_id);

CREATE INDEX sessions_user_active_idx
    ON sessions (user_id, last_activity_at)
    WHERE revoked_at IS NULL;


CREATE TABLE refresh_tokens (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    session_id UUID NOT NULL
        REFERENCES sessions(id) ON DELETE CASCADE,

    token_hash TEXT NOT NULL,

    issued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    replaced_by UUID
        REFERENCES refresh_tokens(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX refresh_tokens_token_hash_idx
    ON refresh_tokens (token_hash);

CREATE INDEX refresh_tokens_session_idx
    ON refresh_tokens (session_id);