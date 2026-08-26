CREATE TABLE realms (
    id           uuid           NOT NULL,
    slug         varchar(63)    NOT NULL,
    display_name varchar(255)   NOT NULL,
    issuer       varchar(255)   NOT NULL,
    status       varchar(16)    NOT NULL,
    created_at   timestamptz(6) NOT NULL,
    updated_at   timestamptz(6) NOT NULL,
    CONSTRAINT pk_realms PRIMARY KEY (id),
    CONSTRAINT uq_realms_slug UNIQUE (slug),
    CONSTRAINT uq_realms_issuer UNIQUE (issuer),
    CONSTRAINT ck_realms_status CHECK (status IN ('active', 'disabled', 'archived'))
);
