ALTER TABLE realms
    ADD COLUMN access_token_ttl INTEGER NOT NULL DEFAULT 300
        CONSTRAINT ck_realms_access_token_ttl CHECK (access_token_ttl > 0);
