CREATE TABLE identities (
    id         CHAR(36)     NOT NULL,
    realm_id   CHAR(36)     NOT NULL,
    email      VARCHAR(254) NOT NULL,
    username   VARCHAR(64)  NULL,
    status     VARCHAR(16)  NOT NULL,
    created_at DATETIME(6)  NOT NULL,
    updated_at DATETIME(6)  NOT NULL,
    CONSTRAINT pk_identities PRIMARY KEY (id),
    CONSTRAINT fk_identities_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT ck_identities_status CHECK (status IN ('active', 'disabled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;
