ALTER TABLE saved_items
    ADD COLUMN price_amount NUMERIC,
    ADD COLUMN price_currency VARCHAR(3);