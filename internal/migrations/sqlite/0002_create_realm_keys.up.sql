CREATE TABLE realm_keys (
    id            TEXT NOT NULL,
    realm_id      TEXT NOT NULL,
    kid           TEXT NOT NULL,
    purpose       TEXT NOT NULL,
    algorithm     TEXT NOT NULL,
    status        TEXT NOT NULL,
    active_marker TEXT NULL,
    public_key    TEXT NOT NULL,
    private_key   TEXT NOT NULL,
    kek_id        TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    CONSTRAINT pk_realm_keys PRIMARY KEY (id),
    CONSTRAINT fk_realm_keys_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT ck_realm_keys_purpose   CHECK (purpose IN ('sig')),
    CONSTRAINT ck_realm_keys_algorithm CHECK (algorithm IN ('RS256', 'ES256')),
    CONSTRAINT ck_realm_keys_status    CHECK (status IN ('active', 'passive', 'disabled')),
    CONSTRAINT ck_realm_keys_marker    CHECK ((status = 'active') = (active_marker IS NOT NULL))
) STRICT;
