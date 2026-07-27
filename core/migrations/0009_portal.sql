-- The end-user portal: a second kind of subject, alongside admins.
--
-- Everything here is deliberately separate from the admin tables. A portal
-- session must never resolve to an admin identity, and the cheapest way to
-- guarantee that is for the two to have no table in common — the guarantee is
-- then a table name rather than a WHERE clause somebody has to remember.

-- Login identity for a proxy user. A missing row means exactly what it looks
-- like: an operator-created subscriber with no way to sign in. That is the
-- reason this is its own table and not columns on `users` — no sentinel value
-- is needed to express it.
--
-- The second reason is hotter: store.userCols is read by every node assembly,
-- every subscription fetch and every sweep. Putting password_hash in that
-- struct would place credential material on the busiest read path in the
-- panel, one field away from the JSON the admin API returns.
CREATE TABLE user_accounts (
    user_id        TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    -- The person's email address, used as the login handle. NOT the same thing
    -- as credentials.email, which is Xray's per-credential stats key
    -- (name.userID@profileID.nodeID), nor the {{user.email}} of templates.
    email          TEXT    NOT NULL,
    password_hash  TEXT    NOT NULL,
    email_verified INTEGER NOT NULL DEFAULT 0,
    -- Portal access, deliberately independent of users.enabled: an operator
    -- may want to stop someone proxying while they can still sign in and read
    -- why, or lock them out of the portal without touching their service.
    disabled       INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    last_login     INTEGER NOT NULL DEFAULT 0
);

-- Addresses are compared lowercased and trimmed in Go before they reach here.
-- A deliberate departure from RFC 5321, which makes the local part
-- case-sensitive: every real provider folds case, and not folding it produces
-- duplicate accounts and "my password doesn't work" tickets.
CREATE UNIQUE INDEX idx_user_accounts_email ON user_accounts (email);

-- Portal sessions. Never rows in admin_sessions: AdminBySession joins admins
-- on admin_id, so a portal token physically cannot resolve there. That is the
-- whole point of the separation.
CREATE TABLE portal_sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX idx_portal_sessions_user   ON portal_sessions (user_id);
CREATE INDEX idx_portal_sessions_expiry ON portal_sessions (expires_at);

-- Short-lived tokens for claiming an account and resetting a password.
--
-- Cannot reuse auth_challenges: its admin_id is NOT NULL REFERENCES admins(id),
-- and the DSN enables foreign_keys. That constraint is doing real type-guard
-- work today and should not be weakened to save a table.
--
-- The purposes not used yet are listed because SQLite cannot ALTER a CHECK
-- constraint: adding a second factor for end users later is the difference
-- between one line of SQL and rebuilding the table.
CREATE TABLE portal_challenges (
    token_hash TEXT PRIMARY KEY,
    user_id    TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose    TEXT    NOT NULL CHECK (purpose IN
        ('claim', 'password_reset', 'email_verify',
         'login', 'email_code', 'webauthn_login', 'webauthn_register')),
    data       TEXT    NOT NULL DEFAULT '',
    attempts   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX idx_portal_challenges_user   ON portal_challenges (user_id, purpose);
CREATE INDEX idx_portal_challenges_expiry ON portal_challenges (expires_at);

-- The subscription token, recoverable.
--
-- This reverses what 0003_users.sql wrote down ("the token itself is shown
-- once at creation and on explicit reset, never stored"). A portal whose main
-- job is handing someone their subscription link cannot work without it, and
-- the alternative — "regenerate to see it" — breaks every client the person
-- has already configured, every time they look.
--
-- sub_token_hash and its unique index are untouched: /sub/{token} still looks
-- up by hash and still answers 404 for anything unknown. Only the portal's
-- display path opens this.
--
-- Deliberately NOT added to store.userCols. Every ListUsers would otherwise
-- decrypt a secret it does not need, and a single row sealed under a rotated
-- CHIRAL_SECRET_KEY would fail the whole query.
ALTER TABLE users ADD COLUMN sub_token_enc TEXT NOT NULL DEFAULT '';

-- Customer-facing node name ("Japan · Tokyo 01").
--
-- Empty does NOT fall back to nodes.name. After this migration every existing
-- node has an empty display_name, and createNode only accepts {"name"}, so a
-- fallback would mean the portal's first day ships internal names like
-- bwh-lax-3 — which encode the provider and datacentre — to every registered
-- user. The failure direction has to be "missing label", not "leaked label".
ALTER TABLE nodes ADD COLUMN display_name TEXT NOT NULL DEFAULT '';

-- Which kind of subject performed an audited action.
--
-- Existing rows are all admin actions, so the default backfills correctly. A
-- column rather than an 'portal.' action-name convention because store.NewID()
-- draws admin and user ids from the same 8-byte space: without actor_type,
-- ?actor_id= is ambiguous about which table to look in, and ambiguity in the
-- audit trail is a defect rather than an inconvenience.
ALTER TABLE audit_log ADD COLUMN actor_type TEXT NOT NULL DEFAULT 'admin';
