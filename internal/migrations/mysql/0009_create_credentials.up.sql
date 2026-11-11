CREATE TABLE credentials (
    id           CHAR(36)     NOT NULL,
    realm_id     CHAR(36)     NOT NULL,
    identity_id  CHAR(36)     NOT NULL,
    type         VARCHAR(16)  NOT NULL,
    hash         VARCHAR(255) NOT NULL,
    must_change  TINYINT(1)   NOT NULL,
    last_used_at DATETIME(6)  NULL,
    created_at   DATETIME(6)  NOT NULL,
    updated_at   DATETIME(6)  NOT NULL,
    CONSTRAINT pk_credentials PRIMARY KEY (id),
    CONSTRAINT fk_credentials_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT fk_credentials_identity FOREIGN KEY (identity_id)
        REFERENCES identities (id) ON DELETE CASCADE,
    CONSTRAINT ck_credentials_type        CHECK (type IN ('password')),
    CONSTRAINT ck_credentials_must_change CHECK (must_change IN (0, 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;
