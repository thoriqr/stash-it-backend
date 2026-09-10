DROP INDEX registration_continuations_pending_registration_unique_idx;

ALTER TABLE verification_requests
DROP CONSTRAINT verification_requests_status_check;

ALTER TABLE verification_requests
ADD CONSTRAINT verification_requests_status_check
CHECK (
    status IN (
        'pending',
        'verified',
        'expired'
    )
);