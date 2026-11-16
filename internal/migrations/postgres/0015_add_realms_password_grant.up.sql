ALTER TABLE realms
    ADD COLUMN password_grant boolean NOT NULL DEFAULT false;
