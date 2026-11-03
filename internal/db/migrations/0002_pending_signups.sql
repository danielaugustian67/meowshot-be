-- A sign-up waiting for its email code. The users row is only created once the code is confirmed,
-- so an unconfirmed sign-up never holds an email or username.
CREATE TABLE pending_signups (
    id            BIGSERIAL PRIMARY KEY,
    token_hash    BYTEA       NOT NULL UNIQUE,
    email         TEXT        NOT NULL,
    username      TEXT        NOT NULL,
    password_hash TEXT        NOT NULL,
    code_hash     BYTEA       NOT NULL,
    attempts      INT         NOT NULL DEFAULT 0,
    sends         INT         NOT NULL DEFAULT 1,
    last_sent_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX pending_signups_email_idx ON pending_signups (email, created_at DESC);
