CREATE TABLE realm_keys (
    id            CHAR(36)      NOT NULL,
    realm_id      CHAR(36)      NOT NULL,
    kid           VARCHAR(64)   NOT NULL,
    purpose       VARCHAR(8)    NOT NULL,
    algorithm     VARCHAR(8)    NOT NULL,
    status        VARCHAR(16)   NOT NULL,
    active_marker CHAR(1)       NULL,
    public_key    VARCHAR(1024) NOT NULL,
    private_key   VARCHAR(4096) NOT NULL,
    kek_id        VARCHAR(64)   NOT NULL,
    created_at    DATETIME(6)   NOT NULL,
    updated_at    DATETIME(6)   NOT NULL,
    CONSTRAINT pk_realm_keys PRIMARY KEY (id),
    CONSTRAINT fk_realm_keys_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT ck_realm_keys_purpose   CHECK (purpose IN ('sig')),
    CONSTRAINT ck_realm_keys_algorithm CHECK (algorithm IN ('RS256', 'ES256')),
    CONSTRAINT ck_realm_keys_status    CHECK (status IN ('active', 'passive', 'disabled')),
    CONSTRAINT ck_realm_keys_marker    CHECK ((status = 'active') = (active_marker IS NOT NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;
