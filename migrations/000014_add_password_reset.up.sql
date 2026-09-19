ALTER TABLE verification_requests
DROP CONSTRAINT verification_requests_subject_type_check;

ALTER TABLE verification_requests
ADD CONSTRAINT verification_requests_subject_type_check
CHECK (
    subject_type IN (
        'pending_registration',
        'pending_password_reset'
    )
);

ALTER TABLE verification_requests
DROP CONSTRAINT verification_requests_purpose_check;

ALTER TABLE verification_requests
ADD CONSTRAINT verification_requests_purpose_check
CHECK (
    purpose IN (
        'registration',
        'password_reset'
    )
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

CREATE UNIQUE INDEX password_reset_continuations_token_hash_idx
    ON password_reset_continuations (token_hash);

CREATE INDEX password_reset_continuations_pending_reset_idx
    ON password_reset_continuations (pending_password_reset_id);