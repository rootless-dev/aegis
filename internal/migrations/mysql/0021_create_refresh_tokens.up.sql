CREATE TABLE refresh_tokens (
    id         CHAR(36)    NOT NULL,
    realm_id   CHAR(36)    NOT NULL,
    session_id CHAR(36)    NOT NULL,
    parent_id  CHAR(36)    NULL,
    token_hash CHAR(64)    NOT NULL,
    created_at DATETIME(6) NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    used_at    DATETIME(6) NULL,
    CONSTRAINT pk_refresh_tokens PRIMARY KEY (id),
    CONSTRAINT fk_refresh_tokens_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT fk_refresh_tokens_session FOREIGN KEY (session_id)
        REFERENCES sessions (id) ON DELETE CASCADE,
    CONSTRAINT fk_refresh_tokens_parent FOREIGN KEY (parent_id)
        REFERENCES refresh_tokens (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;
