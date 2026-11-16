CREATE TABLE sessions (
    id           CHAR(36)     NOT NULL,
    realm_id     CHAR(36)     NOT NULL,
    identity_id  CHAR(36)     NOT NULL,
    ip           VARCHAR(45)  NULL,
    user_agent   VARCHAR(255) NULL,
    created_at   DATETIME(6)  NOT NULL,
    last_seen_at DATETIME(6)  NOT NULL,
    expires_at   DATETIME(6)  NOT NULL,
    ended_at     DATETIME(6)  NULL,
    end_reason   VARCHAR(16)  NULL,
    CONSTRAINT pk_sessions PRIMARY KEY (id),
    CONSTRAINT fk_sessions_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT fk_sessions_identity FOREIGN KEY (identity_id)
        REFERENCES identities (id) ON DELETE CASCADE,
    CONSTRAINT ck_sessions_end_reason CHECK (end_reason IN ('revoked', 'reuse', 'realm_disabled')),
    CONSTRAINT ck_sessions_ended      CHECK ((ended_at IS NULL) = (end_reason IS NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;
