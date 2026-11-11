ALTER TABLE realms
    ADD COLUMN session_max_ttl INT NOT NULL DEFAULT 36000,
    ADD CONSTRAINT ck_realms_session_max_ttl CHECK (session_max_ttl > 0);
