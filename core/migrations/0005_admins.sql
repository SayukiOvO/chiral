-- Admin accounts, their sessions, and the audit trail.
--
-- Until now the panel had exactly one credential: a bearer token from the
-- environment. That stays as a break-glass path (an operator who has lost
-- every password still has their compose file), but real accounts sit on top
-- of it so actions have a name attached.

CREATE TABLE admins (
    id       TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    -- PBKDF2-HMAC-SHA256, stored as "pbkdf2$<iterations>$<salt>$<hash>".
    -- Unlike the opaque tokens elsewhere in the panel, this protects a value a
    -- human chose, so it needs a slow hash rather than a single SHA-256.
    password_hash TEXT NOT NULL,
    -- superadmin: everything, including managing admins.
    -- operator:   manage nodes, profiles, variables and users.
    -- viewer:     read only.
    role       TEXT NOT NULL CHECK (role IN ('superadmin', 'operator', 'viewer')),
    disabled   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    last_login INTEGER NOT NULL DEFAULT 0
);

-- Only the hash of a session token is stored, like every other credential
-- here: a leaked database must not yield a usable session.
CREATE TABLE admin_sessions (
    token_hash TEXT PRIMARY KEY,
    admin_id   TEXT NOT NULL REFERENCES admins (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE INDEX idx_admin_sessions_admin ON admin_sessions (admin_id);
CREATE INDEX idx_admin_sessions_expiry ON admin_sessions (expires_at);

-- What happened, who did it, and to what. Deliberately append-only in
-- practice: nothing in the panel updates or deletes a row here except
-- retention pruning.
--
-- actor_name is denormalised on purpose. An audit trail that stops naming the
-- person once their account is deleted is not much of an audit trail, so the
-- name is captured at the time of the action rather than joined later.
CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          INTEGER NOT NULL,
    actor_id    TEXT NOT NULL DEFAULT '',
    actor_name  TEXT NOT NULL,
    action      TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id   TEXT NOT NULL DEFAULT '',
    target_name TEXT NOT NULL DEFAULT '',
    detail      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_audit_log_at ON audit_log (at);
CREATE INDEX idx_audit_log_actor ON audit_log (actor_id, at);
