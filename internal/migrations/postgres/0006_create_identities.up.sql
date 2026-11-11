CREATE TABLE identities (
    id         uuid           NOT NULL,
    realm_id   uuid           NOT NULL,
    email      varchar(254)   NOT NULL,
    username   varchar(64)    NULL,
    status     varchar(16)    NOT NULL,
    created_at timestamptz(6) NOT NULL,
    updated_at timestamptz(6) NOT NULL,
    CONSTRAINT pk_identities PRIMARY KEY (id),
    CONSTRAINT fk_identities_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT ck_identities_status CHECK (status IN ('active', 'disabled'))
);
