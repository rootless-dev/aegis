CREATE TABLE credentials (
    id           TEXT    NOT NULL,
    realm_id     TEXT    NOT NULL,
    identity_id  TEXT    NOT NULL,
    type         TEXT    NOT NULL,
    hash         TEXT    NOT NULL,
    must_change  INTEGER NOT NULL,
    last_used_at TEXT    NULL,
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL,
    CONSTRAINT pk_credentials PRIMARY KEY (id),
    CONSTRAINT fk_credentials_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT fk_credentials_identity FOREIGN KEY (identity_id)
        REFERENCES identities (id) ON DELETE CASCADE,
    CONSTRAINT ck_credentials_type        CHECK (type IN ('password')),
    CONSTRAINT ck_credentials_must_change CHECK (must_change IN (0, 1))
) STRICT;
