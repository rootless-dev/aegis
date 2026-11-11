ALTER TABLE realms
    ADD COLUMN session_max_ttl INTEGER NOT NULL DEFAULT 36000
        CONSTRAINT ck_realms_session_max_ttl CHECK (session_max_ttl > 0);
