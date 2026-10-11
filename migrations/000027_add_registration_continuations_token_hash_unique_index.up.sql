CREATE UNIQUE INDEX registration_continuations_token_hash_idx
    ON registration_continuations (token_hash);