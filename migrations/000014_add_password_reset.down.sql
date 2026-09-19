DROP TABLE password_reset_continuations;

DROP TABLE pending_password_resets;

ALTER TABLE verification_requests
DROP CONSTRAINT verification_requests_subject_type_check;

ALTER TABLE verification_requests
ADD CONSTRAINT verification_requests_subject_type_check
CHECK (
    subject_type = 'pending_registration'
);

ALTER TABLE verification_requests
DROP CONSTRAINT verification_requests_purpose_check;

ALTER TABLE verification_requests
ADD CONSTRAINT verification_requests_purpose_check
CHECK (
    purpose = 'registration'
);