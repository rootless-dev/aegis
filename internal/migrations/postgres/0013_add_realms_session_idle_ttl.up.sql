ALTER TABLE realms
    ADD COLUMN session_idle_ttl integer NOT NULL DEFAULT 1800
        CONSTRAINT ck_realms_session_idle_ttl CHECK (session_idle_ttl > 0);
