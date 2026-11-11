CREATE TABLE credentials (
    id           uuid           NOT NULL,
    realm_id     uuid           NOT NULL,
    identity_id  uuid           NOT NULL,
    type         varchar(16)    NOT NULL,
    hash         varchar(255)   NOT NULL,
    must_change  boolean        NOT NULL,
    last_used_at timestamptz(6) NULL,
    created_at   timestamptz(6) NOT NULL,
    updated_at   timestamptz(6) NOT NULL,
    CONSTRAINT pk_credentials PRIMARY KEY (id),
    CONSTRAINT fk_credentials_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT fk_credentials_identity FOREIGN KEY (identity_id)
        REFERENCES identities (id) ON DELETE CASCADE,
    CONSTRAINT ck_credentials_type CHECK (type IN ('password'))
);
