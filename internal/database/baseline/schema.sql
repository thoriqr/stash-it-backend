-- Baseline schema
-- Represents the final database state after migrations 001-016.

-- ============================================================
-- Functions
-- ============================================================

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;


-- ============================================================
-- Tables
-- ============================================================

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    email TEXT NOT NULL,
    display_name TEXT NOT NULL,
    email_verified_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT users_email_unique UNIQUE (email)
);


CREATE TABLE password_credentials (
    user_id UUID PRIMARY KEY
        REFERENCES users(id) ON DELETE CASCADE,

    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


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

CREATE TABLE pending_registrations (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    email TEXT NOT NULL,
    registration_type TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,

    CONSTRAINT pending_registrations_registration_type_check
        CHECK (
            registration_type IN (
                'manual',
                'social'
            )
        ),

    CONSTRAINT pending_registrations_status_check
        CHECK (
            status IN (
                'pending',
                'completed',
                'expired'
            )
        )
);


CREATE TABLE verification_requests (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    subject_type TEXT NOT NULL,
    subject_id UUID NOT NULL,
    purpose TEXT NOT NULL,
    status TEXT NOT NULL,
    pin_issued_count INTEGER NOT NULL DEFAULT 0,
    last_sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT verification_requests_subject_type_check
        CHECK (
            subject_type IN (
                'pending_registration',
                'pending_password_reset'
            )
        ),

    CONSTRAINT verification_requests_purpose_check
        CHECK (
            purpose IN (
                'registration',
                'password_reset'
            )
        ),

    CONSTRAINT verification_requests_status_check
        CHECK (
            status IN (
                'pending',
                'verified'
            )
        ),

    CONSTRAINT verification_requests_pin_issued_count_check
        CHECK (pin_issued_count >= 0)
);


CREATE TABLE verification_codes (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    verification_request_id UUID NOT NULL
        REFERENCES verification_requests(id) ON DELETE CASCADE,

    code_hash TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    invalidated_at TIMESTAMPTZ,

    CONSTRAINT verification_codes_attempts_check
        CHECK (attempts >= 0)
);


CREATE TABLE pending_password_resets (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    email TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,

    CONSTRAINT pending_password_resets_status_check
        CHECK (
            status IN (
                'pending',
                'completed',
                'expired'
            )
        )
);


CREATE TABLE password_reset_continuations (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    pending_password_reset_id UUID NOT NULL
        REFERENCES pending_password_resets(id) ON DELETE CASCADE,

    token_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE registration_continuations (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    pending_registration_id UUID NOT NULL
        REFERENCES pending_registrations(id) ON DELETE CASCADE,

    token_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


-- ============================================================
-- Indexes
-- ============================================================

CREATE INDEX verification_requests_subject_idx
    ON verification_requests (subject_type, subject_id);


CREATE INDEX verification_codes_request_idx
    ON verification_codes (verification_request_id);


CREATE UNIQUE INDEX verification_codes_one_active_per_request_idx
    ON verification_codes (verification_request_id)
    WHERE consumed_at IS NULL
      AND invalidated_at IS NULL;


CREATE INDEX registration_continuations_pending_registration_idx
    ON registration_continuations (pending_registration_id);


CREATE UNIQUE INDEX registration_continuations_pending_registration_unique_idx
    ON registration_continuations (pending_registration_id);


CREATE UNIQUE INDEX password_reset_continuations_token_hash_idx
    ON password_reset_continuations (token_hash);


CREATE INDEX password_reset_continuations_pending_reset_idx
    ON password_reset_continuations (pending_password_reset_id);


CREATE UNIQUE INDEX pending_password_resets_email_pending_idx
    ON pending_password_resets (email)
    WHERE status = 'pending';

CREATE UNIQUE INDEX pending_registrations_email_idx
    ON pending_registrations (email)
    WHERE status IN ('pending', 'completed');


-- ============================================================
-- Triggers
-- ============================================================

CREATE TRIGGER users_set_updated_at
BEFORE UPDATE ON users
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();


CREATE TRIGGER password_credentials_set_updated_at
BEFORE UPDATE ON password_credentials
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();