ALTER TABLE realms
    ADD COLUMN password_grant TINYINT(1) NOT NULL DEFAULT 0,
    ADD CONSTRAINT ck_realms_password_grant CHECK (password_grant IN (0, 1));
