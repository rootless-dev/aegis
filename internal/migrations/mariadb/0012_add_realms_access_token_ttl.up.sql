-- MariaDB cannot name a CHECK written inside the column definition, so the
-- constraint is a second clause of the same ALTER TABLE on both engines.
ALTER TABLE realms
    ADD COLUMN access_token_ttl INT NOT NULL DEFAULT 300,
    ADD CONSTRAINT ck_realms_access_token_ttl CHECK (access_token_ttl > 0);
