CREATE TABLE realms (
    id           CHAR(36)     NOT NULL,
    slug         VARCHAR(63)  NOT NULL,
    display_name VARCHAR(255) NOT NULL,
    issuer       VARCHAR(255) NOT NULL,
    status       VARCHAR(16)  NOT NULL,
    created_at   DATETIME(6)  NOT NULL,
    updated_at   DATETIME(6)  NOT NULL,
    CONSTRAINT pk_realms PRIMARY KEY (id),
    CONSTRAINT uq_realms_slug UNIQUE (slug),
    CONSTRAINT uq_realms_issuer UNIQUE (issuer),
    CONSTRAINT ck_realms_status CHECK (status IN ('active', 'disabled', 'archived'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;
