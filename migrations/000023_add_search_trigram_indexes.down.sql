-- Reverse only what 000023 introduced, in reverse dependency order.

DROP INDEX collections_search_idx;

DROP INDEX saved_items_search_idx;

-- Extensions are deliberately left in place. DROP EXTENSION would fail on a
-- database where another object already depends on either one, and the
-- extensions are harmless once unused.