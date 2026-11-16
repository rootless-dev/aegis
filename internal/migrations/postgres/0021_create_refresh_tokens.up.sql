CREATE TABLE refresh_tokens (
    id         uuid           NOT NULL,
    realm_id   uuid           NOT NULL,
    session_id uuid           NOT NULL,
    parent_id  uuid           NULL,
    token_hash char(64)       NOT NULL,
    created_at timestamptz(6) NOT NULL,
    expires_at timestamptz(6) NOT NULL,
    used_at    timestamptz(6) NULL,
    CONSTRAINT pk_refresh_tokens PRIMARY KEY (id),
    CONSTRAINT fk_refresh_tokens_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT fk_refresh_tokens_session FOREIGN KEY (session_id)
        REFERENCES sessions (id) ON DELETE CASCADE,
    CONSTRAINT fk_refresh_tokens_parent FOREIGN KEY (parent_id)
        REFERENCES refresh_tokens (id) ON DELETE SET NULL
);
