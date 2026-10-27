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
