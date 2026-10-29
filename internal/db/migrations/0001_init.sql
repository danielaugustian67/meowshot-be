-- Meowshot initial schema (PRD v0.3, section 9).

CREATE TABLE users (
    id             BIGSERIAL PRIMARY KEY,
    username       TEXT        NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9_.]{3,30}$'),
    email          TEXT        NOT NULL UNIQUE,
    email_verified BOOLEAN     NOT NULL DEFAULT FALSE,
    password_hash  TEXT,
    google_sub     TEXT UNIQUE,
    avatar_key     TEXT,
    bio            TEXT        NOT NULL DEFAULT '',
    role           TEXT        NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    suspended_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE refresh_tokens (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash BYTEA       NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens (user_id);

CREATE TABLE password_resets (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash  BYTEA       NOT NULL,
    attempts   INT         NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX password_resets_user_idx ON password_resets (user_id, created_at DESC);

-- One row per pair, normalised so (A,B) and (B,A) cannot both exist.
CREATE TABLE friendships (
    id           BIGSERIAL PRIMARY KEY,
    user_low     BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    user_high    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    requester_id BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    status       TEXT        NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'ACCEPTED')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    accepted_at  TIMESTAMPTZ,
    CHECK (user_low < user_high),
    CHECK (requester_id IN (user_low, user_high)),
    UNIQUE (user_low, user_high)
);
CREATE INDEX friendships_high_idx ON friendships (user_high, status);

CREATE TABLE blocks (
    blocker_id BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    blocked_id BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (blocker_id, blocked_id),
    CHECK (blocker_id <> blocked_id)
);
CREATE INDEX blocks_blocked_idx ON blocks (blocked_id);

CREATE TABLE cats (
    id             BIGSERIAL PRIMARY KEY,
    user_id        BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name           TEXT,
    notes          TEXT,
    cover_photo_id BIGINT,
    is_favorite    BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX cats_user_idx ON cats (user_id, created_at DESC);

CREATE TABLE sightings (
    id         BIGSERIAL PRIMARY KEY,
    cat_id     BIGINT           NOT NULL REFERENCES cats (id) ON DELETE CASCADE,
    user_id    BIGINT           NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    lat        DOUBLE PRECISION CHECK (lat BETWEEN -90 AND 90),
    lng        DOUBLE PRECISION CHECK (lng BETWEEN -180 AND 180),
    label      TEXT,
    source     TEXT             NOT NULL DEFAULT 'manual' CHECK (source IN ('exif', 'device', 'manual')),
    seen_at    TIMESTAMPTZ      NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ      NOT NULL DEFAULT now(),
    CHECK ((lat IS NULL) = (lng IS NULL))
);
CREATE INDEX sightings_cat_idx ON sightings (cat_id, seen_at DESC);
CREATE INDEX sightings_user_geo_idx ON sightings (user_id, lat, lng);

CREATE TABLE posts (
    id                BIGSERIAL PRIMARY KEY,
    user_id           BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    cat_id            BIGINT      NOT NULL REFERENCES cats (id) ON DELETE CASCADE,
    sighting_id       BIGINT      REFERENCES sightings (id) ON DELETE SET NULL,
    caption           TEXT        NOT NULL DEFAULT '',
    visibility        TEXT        NOT NULL DEFAULT 'FRIENDS' CHECK (visibility IN ('PRIVATE', 'FRIENDS', 'PUBLIC')),
    loc_precision     TEXT        NOT NULL DEFAULT 'APPROX' CHECK (loc_precision IN ('EXACT', 'APPROX', 'HIDDEN')),
    comments_disabled BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX posts_user_idx ON posts (user_id, id DESC);
CREATE INDEX posts_visibility_idx ON posts (visibility, id DESC);
CREATE INDEX posts_cat_idx ON posts (cat_id);

CREATE TABLE photos (
    id            BIGSERIAL PRIMARY KEY,
    user_id       BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    cat_id        BIGINT      NOT NULL REFERENCES cats (id) ON DELETE CASCADE,
    post_id       BIGINT      REFERENCES posts (id) ON DELETE SET NULL,
    post_position SMALLINT,
    key           TEXT        NOT NULL,
    thumb_key     TEXT        NOT NULL,
    width         INT         NOT NULL,
    height        INT         NOT NULL,
    caption       TEXT        NOT NULL DEFAULT '',
    taken_at      TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX photos_cat_idx ON photos (cat_id, id DESC);
CREATE INDEX photos_post_idx ON photos (post_id, post_position);

ALTER TABLE cats
    ADD CONSTRAINT cats_cover_fk FOREIGN KEY (cover_photo_id) REFERENCES photos (id) ON DELETE SET NULL;

CREATE TABLE reactions (
    post_id    BIGINT      NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    emoji      TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (post_id, user_id)
);

CREATE TABLE comments (
    id         BIGSERIAL PRIMARY KEY,
    post_id    BIGINT      NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    body       TEXT        NOT NULL CHECK (length(body) BETWEEN 1 AND 1000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX comments_post_idx ON comments (post_id, id);

CREATE TABLE notifications (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    actor_id   BIGINT      REFERENCES users (id) ON DELETE CASCADE,
    type       TEXT        NOT NULL CHECK (type IN ('friend_request', 'friend_accepted', 'reaction', 'comment')),
    ref_id     BIGINT,
    read_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX notifications_user_idx ON notifications (user_id, id DESC);
CREATE INDEX notifications_unread_idx ON notifications (user_id) WHERE read_at IS NULL;

CREATE TABLE reports (
    id          BIGSERIAL PRIMARY KEY,
    reporter_id BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    target_type TEXT        NOT NULL CHECK (target_type IN ('POST', 'COMMENT', 'USER')),
    target_id   BIGINT      NOT NULL,
    reason      TEXT        NOT NULL CHECK (length(reason) BETWEEN 1 AND 1000),
    status      TEXT        NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'RESOLVED', 'DISMISSED')),
    resolved_by BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    resolved_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX reports_status_idx ON reports (status, created_at);
