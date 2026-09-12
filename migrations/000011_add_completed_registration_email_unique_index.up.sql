CREATE UNIQUE INDEX pending_registrations_completed_email_idx
ON pending_registrations (email)
WHERE status = 'completed';