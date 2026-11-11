CREATE TABLE sessions (
    id           uuid           NOT NULL,
    realm_id     uuid           NOT NULL,
    identity_id  uuid           NOT NULL,
    ip           varchar(45)    NULL,
    user_agent   varchar(255)   NULL,
    created_at   timestamptz(6) NOT NULL,
    last_seen_at timestamptz(6) NOT NULL,
    expires_at   timestamptz(6) NOT NULL,
    ended_at     timestamptz(6) NULL,
    end_reason   varchar(16)    NULL,
    CONSTRAINT pk_sessions PRIMARY KEY (id),
    CONSTRAINT fk_sessions_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT fk_sessions_identity FOREIGN KEY (identity_id)
        REFERENCES identities (id) ON DELETE CASCADE,
    CONSTRAINT ck_sessions_end_reason CHECK (end_reason IN ('revoked', 'reuse', 'realm_disabled')),
    CONSTRAINT ck_sessions_ended      CHECK ((ended_at IS NULL) = (end_reason IS NULL))
);
