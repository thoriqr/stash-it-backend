ALTER TABLE pending_social_identities
ADD COLUMN email_snapshot TEXT,
ADD COLUMN display_name_snapshot TEXT;

ALTER TABLE auth_identities
ADD COLUMN email_snapshot TEXT,
ADD COLUMN display_name_snapshot TEXT;