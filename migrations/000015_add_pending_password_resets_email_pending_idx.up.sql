CREATE UNIQUE INDEX pending_password_resets_email_pending_idx
    ON pending_password_resets (email)
    WHERE status = 'pending';