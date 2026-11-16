ALTER TABLE realms
    ADD COLUMN password_grant INTEGER NOT NULL DEFAULT 0
        CONSTRAINT ck_realms_password_grant CHECK (password_grant IN (0, 1));
