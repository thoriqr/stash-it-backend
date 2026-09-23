CREATE TABLE pending_social_identities (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    pending_registration_id UUID NOT NULL,

    provider VARCHAR(32) NOT NULL,
    provider_subject VARCHAR(255) NOT NULL,

    display_name TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT pending_social_identities_pending_registration_fk
        FOREIGN KEY (pending_registration_id)
        REFERENCES pending_registrations(id)
        ON DELETE CASCADE,

    CONSTRAINT pending_social_identities_pending_registration_unique
        UNIQUE (pending_registration_id)
);

CREATE TABLE auth_identities (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    user_id UUID NOT NULL,

    provider VARCHAR(32) NOT NULL,
    provider_subject VARCHAR(255) NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT auth_identities_user_fk
        FOREIGN KEY (user_id)
        REFERENCES users(id)
        ON DELETE CASCADE,

    CONSTRAINT auth_identities_provider_subject_unique
        UNIQUE (provider, provider_subject)
);