CREATE UNIQUE INDEX verification_codes_one_active_per_request_idx
ON verification_codes (verification_request_id)
WHERE consumed_at IS NULL
  AND invalidated_at IS NULL;