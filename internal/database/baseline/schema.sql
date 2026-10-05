-- Baseline schema
-- Represents the final database state after migrations 001-023.
-- Source of truth: the cumulative effect of migrations/000001..000023 (up only).

-- ============================================================
-- Extensions
-- ============================================================

-- pg_trgm supplies the gin_trgm_ops operator class and the word_similarity
-- function that global search matches and ranks with.
--
-- btree_gin supplies the uuid GIN operator class, which has no default. Without
-- it a composite GIN index over user_id cannot be created at all.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE EXTENSION IF NOT EXISTS btree_gin;


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


CREATE TABLE auth_identities (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    user_id UUID NOT NULL,

    provider VARCHAR(32) NOT NULL,
    provider_subject VARCHAR(255) NOT NULL,

    email_snapshot TEXT,
    display_name_snapshot TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT auth_identities_user_fk
        FOREIGN KEY (user_id)
        REFERENCES users(id)
        ON DELETE CASCADE,

    CONSTRAINT auth_identities_provider_subject_unique
        UNIQUE (provider, provider_subject)
);


CREATE TABLE password_credentials (
    user_id UUID PRIMARY KEY
        REFERENCES users(id) ON DELETE CASCADE,

    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


-- Every user is expected to own exactly one 'Unsorted' system collection
-- (type = 'system', system_key = 'unsorted'), which is where newly saved items
-- land. migration 000022 created them for the users that existed at the time;
-- this baseline is a schema snapshot only and seeds no rows.


CREATE TABLE collections (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    user_id UUID NOT NULL
        REFERENCES users(id) ON DELETE CASCADE,

    name TEXT NOT NULL,

    type TEXT NOT NULL,

    system_key TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT collections_type_check
        CHECK (
            type IN (
                'system',
                'user'
            )
        ),

    -- A system collection is identified by system_key, never by its display
    -- name: system collections must carry a key, user collections must not.
    CONSTRAINT collections_system_key_check
        CHECK (
            (type = 'system' AND system_key IS NOT NULL)
            OR (type = 'user' AND system_key IS NULL)
        )
);


CREATE TABLE saved_items (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    user_id UUID NOT NULL
        REFERENCES users(id) ON DELETE CASCADE,

    url TEXT NOT NULL,

    domain TEXT,
    platform TEXT,
    title TEXT,

    -- Optional enriched metadata. Both stay NULL until background enrichment
    -- runs, and either may still be NULL afterwards because a page is not
    -- required to expose a description or a preview image.
    description TEXT,
    image_url TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    collection_id UUID NOT NULL,

    -- Written only by the background enrichment phase. domain is derived from
    -- the URL at save time; platform, title, description and image_url come
    -- from enrichment and remain NULL until it runs.
    --
    -- Worker execution state is deliberately not persisted. An item stays
    -- pending until enrichment completes or fails, so a worker that dies
    -- mid-job needs no 'processing' marker to be reconciled afterwards.
    enrichment_status TEXT NOT NULL DEFAULT 'pending',
    last_enriched_at TIMESTAMPTZ,

    CONSTRAINT saved_items_collection_id_fkey
        FOREIGN KEY (collection_id)
        REFERENCES collections(id)
        ON DELETE RESTRICT,

    CONSTRAINT saved_items_enrichment_status_check
        CHECK (
            enrichment_status IN (
                'pending',
                'completed',
                'failed'
            )
        )
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


CREATE TABLE pending_social_identities (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    pending_registration_id UUID NOT NULL,

    provider VARCHAR(32) NOT NULL,
    provider_subject VARCHAR(255) NOT NULL,

    email_snapshot TEXT,
    display_name_snapshot TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT pending_social_identities_pending_registration_fk
        FOREIGN KEY (pending_registration_id)
        REFERENCES pending_registrations(id)
        ON DELETE CASCADE,

    CONSTRAINT pending_social_identities_pending_registration_unique
        UNIQUE (pending_registration_id)
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


CREATE INDEX saved_items_user_created_at_idx
    ON saved_items (user_id, created_at DESC);


-- Collection names are unique per user, compared case-insensitively and with
-- surrounding whitespace trimmed. This reserves the display name for system
-- collections too, so a user cannot shadow the 'YouTube' system collection.
CREATE UNIQUE INDEX collections_user_name_unique
    ON collections (user_id, lower(btrim(name)));


-- At most one system collection per system_key per user. Partial, so the NULL
-- system_key of user collections never conflicts.
CREATE UNIQUE INDEX collections_system_key_unique
    ON collections (user_id, system_key)
    WHERE system_key IS NOT NULL;


CREATE INDEX collections_user_idx
    ON collections (user_id);


CREATE INDEX saved_items_collection_id_idx
    ON saved_items (collection_id);


-- Global search. Leading user_id keeps the search owner-scoped and lets one
-- index serve both the ownership filter and the trigram filter. btree_gin
-- provides the uuid GIN operator class; pg_trgm provides gin_trgm_ops.
CREATE INDEX saved_items_search_idx
    ON saved_items USING gin (user_id, title gin_trgm_ops);


CREATE INDEX collections_search_idx
    ON collections USING gin (user_id, name gin_trgm_ops);


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


CREATE TRIGGER collections_set_updated_at
BEFORE UPDATE ON collections
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();


CREATE TRIGGER saved_items_set_updated_at
BEFORE UPDATE ON saved_items
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();