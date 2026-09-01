CREATE TABLE users (
  id UUID PRIMARY KEY DEFAULT uuidv7(),
  email TEXT NOT NULL,
  CONSTRAINT users_email_unique UNIQUE(email)
);