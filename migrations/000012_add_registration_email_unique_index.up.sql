DROP INDEX IF EXISTS pending_registrations_active_email_idx;
DROP INDEX IF EXISTS pending_registrations_completed_email_idx;

CREATE UNIQUE INDEX pending_registrations_email_idx
ON pending_registrations (email)
WHERE status IN ('pending', 'completed');