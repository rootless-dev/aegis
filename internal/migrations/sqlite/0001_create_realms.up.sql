CREATE TABLE realms (
    id           TEXT NOT NULL,
    slug         TEXT NOT NULL,
    display_name TEXT NOT NULL,
    issuer       TEXT NOT NULL,
    status       TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    CONSTRAINT pk_realms PRIMARY KEY (id),
    CONSTRAINT uq_realms_slug UNIQUE (slug),
    CONSTRAINT uq_realms_issuer UNIQUE (issuer),
    CONSTRAINT ck_realms_status CHECK (status IN ('active', 'disabled', 'archived'))
) STRICT;
