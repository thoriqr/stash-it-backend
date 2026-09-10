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

CREATE UNIQUE INDEX pending_registrations_active_email_idx
    ON pending_registrations (email)
    WHERE status = 'pending';


CREATE TABLE verification_requests (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    subject_type TEXT NOT NULL,
    subject_id UUID NOT NULL,
    purpose TEXT NOT NULL,
    status TEXT NOT NULL,
    resend_count INTEGER NOT NULL DEFAULT 0,
    last_sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT verification_requests_subject_type_check
        CHECK (
            subject_type IN (
                'pending_registration'
            )
        ),

    CONSTRAINT verification_requests_purpose_check
        CHECK (
            purpose IN (
                'registration'
            )
        ),

    CONSTRAINT verification_requests_status_check
        CHECK (
            status IN (
                'pending',
                'verified',
                'expired'
            )
        ),

    CONSTRAINT verification_requests_resend_count_check
        CHECK (resend_count >= 0)
);

CREATE INDEX verification_requests_subject_idx
    ON verification_requests (subject_type, subject_id);


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

CREATE INDEX verification_codes_request_idx
    ON verification_codes (verification_request_id);