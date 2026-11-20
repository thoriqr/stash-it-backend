-- Reverse only what 000033 introduced. The set_updated_at function is owned by
-- 000003 and is left untouched, so feedback is the only thing that goes.
DROP TRIGGER feedback_set_updated_at ON feedback;

DROP INDEX feedback_status_created_at_idx;

DROP INDEX feedback_created_at_idx;

DROP TABLE feedback;
