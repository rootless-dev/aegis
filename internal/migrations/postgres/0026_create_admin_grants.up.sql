CREATE TABLE admin_grants (
    id              uuid           NOT NULL,
    realm_id        uuid           NOT NULL,
    identity_id     uuid           NOT NULL,
    target_realm_id uuid           NULL,
    global_marker   char(1)        NULL,
    created_at      timestamptz(6) NOT NULL,
    CONSTRAINT pk_admin_grants PRIMARY KEY (id),
    CONSTRAINT fk_admin_grants_realm FOREIGN KEY (realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT fk_admin_grants_identity FOREIGN KEY (identity_id)
        REFERENCES identities (id) ON DELETE CASCADE,
    CONSTRAINT fk_admin_grants_target FOREIGN KEY (target_realm_id)
        REFERENCES realms (id) ON DELETE CASCADE,
    CONSTRAINT ck_admin_grants_scope CHECK ((target_realm_id IS NULL) = (global_marker IS NOT NULL)),
    CONSTRAINT ck_admin_grants_marker CHECK (global_marker IS NULL OR global_marker = 'y')
);
