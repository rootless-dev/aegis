CREATE TABLE refresh_tokens (
    id         TEXT NOT NULL,
    realm_id   TEXT NOT NULL,
    session_id TEXT NOT NULL,
    parent_id  TEXT NULL,
    token_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at    TEXT NULL,
    CONSTRAINT pk_refresh_tokens PRIMARY KEY (id),
    CONSTRAINT fk_refresh_tokens_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT fk_refresh_tokens_session FOREIGN KEY (session_id)
        REFERENCES sessions (id) ON DELETE CASCADE,
    CONSTRAINT fk_refresh_tokens_parent FOREIGN KEY (parent_id)
        REFERENCES refresh_tokens (id) ON DELETE SET NULL
) STRICT;
