CREATE UNIQUE INDEX uq_admin_grants_target ON admin_grants (identity_id, target_realm_id);
