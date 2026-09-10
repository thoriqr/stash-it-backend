CREATE TABLE registration_continuations (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    pending_registration_id UUID NOT NULL
        REFERENCES pending_registrations(id) ON DELETE CASCADE,

    token_hash TEXT NOT NULL,

    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX registration_continuations_pending_registration_idx
    ON registration_continuations (pending_registration_id);