CREATE UNIQUE INDEX uq_realm_keys_active ON realm_keys (realm_id, purpose, algorithm, active_marker);
