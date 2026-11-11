CREATE TABLE identities (
    id         TEXT NOT NULL,
    realm_id   TEXT NOT NULL,
    email      TEXT NOT NULL,
    username   TEXT NULL,
    status     TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CONSTRAINT pk_identities PRIMARY KEY (id),
    CONSTRAINT fk_identities_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT ck_identities_status CHECK (status IN ('active', 'disabled'))
) STRICT;
