CREATE TABLE realm_keys (
    id            uuid           NOT NULL,
    realm_id      uuid           NOT NULL,
    kid           varchar(64)    NOT NULL,
    purpose       varchar(8)     NOT NULL,
    algorithm     varchar(8)     NOT NULL,
    status        varchar(16)    NOT NULL,
    active_marker char(1)        NULL,
    public_key    varchar(1024)  NOT NULL,
    private_key   varchar(4096)  NOT NULL,
    kek_id        varchar(64)    NOT NULL,
    created_at    timestamptz(6) NOT NULL,
    updated_at    timestamptz(6) NOT NULL,
    CONSTRAINT pk_realm_keys PRIMARY KEY (id),
    CONSTRAINT fk_realm_keys_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT ck_realm_keys_purpose   CHECK (purpose IN ('sig')),
    CONSTRAINT ck_realm_keys_algorithm CHECK (algorithm IN ('RS256', 'ES256')),
    CONSTRAINT ck_realm_keys_status    CHECK (status IN ('active', 'passive', 'disabled')),
    CONSTRAINT ck_realm_keys_marker    CHECK ((status = 'active') = (active_marker IS NOT NULL))
);
