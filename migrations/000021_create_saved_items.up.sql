CREATE TABLE saved_items (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    user_id UUID NOT NULL
        REFERENCES users(id) ON DELETE CASCADE,

    url TEXT NOT NULL,

    domain TEXT,
    platform TEXT,
    title TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX saved_items_user_created_at_idx
    ON saved_items (user_id, created_at DESC);


CREATE TRIGGER saved_items_set_updated_at
BEFORE UPDATE ON saved_items
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();
