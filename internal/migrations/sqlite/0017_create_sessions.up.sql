CREATE TABLE sessions (
    id           TEXT NOT NULL,
    realm_id     TEXT NOT NULL,
    identity_id  TEXT NOT NULL,
    ip           TEXT NULL,
    user_agent   TEXT NULL,
    created_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    ended_at     TEXT NULL,
    end_reason   TEXT NULL,
    CONSTRAINT pk_sessions PRIMARY KEY (id),
    CONSTRAINT fk_sessions_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT fk_sessions_identity FOREIGN KEY (identity_id)
        REFERENCES identities (id) ON DELETE CASCADE,
    CONSTRAINT ck_sessions_end_reason CHECK (end_reason IN ('revoked', 'reuse', 'realm_disabled')),
    CONSTRAINT ck_sessions_ended      CHECK ((ended_at IS NULL) = (end_reason IS NULL))
) STRICT;
