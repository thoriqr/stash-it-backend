CREATE TABLE account_link_confirmations (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    user_id UUID NOT NULL
        REFERENCES users(id)
        ON DELETE CASCADE,

    provider VARCHAR(32) NOT NULL,
    provider_subject VARCHAR(255) NOT NULL,

    email_snapshot TEXT,
    display_name_snapshot TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    confirmed_at TIMESTAMPTZ
);