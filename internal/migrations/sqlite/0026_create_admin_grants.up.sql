CREATE TABLE admin_grants (
    id              TEXT NOT NULL,
    realm_id        TEXT NOT NULL,
    identity_id     TEXT NOT NULL,
    target_realm_id TEXT NULL,
    global_marker   TEXT NULL,
    created_at      TEXT NOT NULL,
    CONSTRAINT pk_admin_grants PRIMARY KEY (id),
    CONSTRAINT fk_admin_grants_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT fk_admin_grants_identity FOREIGN KEY (identity_id)
        REFERENCES identities (id) ON DELETE CASCADE,
    CONSTRAINT fk_admin_grants_target FOREIGN KEY (target_realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT ck_admin_grants_scope CHECK ((target_realm_id IS NULL) = (global_marker IS NOT NULL)),
    CONSTRAINT ck_admin_grants_marker CHECK (global_marker IS NULL OR global_marker = 'y')
) STRICT;
