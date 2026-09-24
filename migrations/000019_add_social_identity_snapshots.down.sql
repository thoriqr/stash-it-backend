ALTER TABLE pending_social_identities
DROP COLUMN email_snapshot,
DROP COLUMN display_name_snapshot;

ALTER TABLE auth_identities
DROP COLUMN email_snapshot,
DROP COLUMN display_name_snapshot;