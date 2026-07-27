-- Users, their subscription, and their per-access-point credentials.
-- See docs/user-management.md.

CREATE TABLE users (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    -- Hash of the subscription token, used to resolve /sub/{token}.
    --
    -- This originally said the token itself was never stored. Migration 0009
    -- reverses that and adds sub_token_enc, a sealed copy, so the end-user
    -- portal can show someone their own link — the alternative was "regenerate
    -- to see it", which breaks every client they have already configured. The
    -- lookup path here is unchanged. See CLAUDE.md §4.11.
    sub_token_hash TEXT NOT NULL,
    -- 0 means unlimited. Counted across every credential the user holds.
    quota_bytes    INTEGER NOT NULL DEFAULT 0,
    used_bytes     INTEGER NOT NULL DEFAULT 0,
    -- 0 means no expiry.
    expires_at     INTEGER NOT NULL DEFAULT 0,
    -- Seconds to push expiry forward by when it lapses; 0 disables renewal.
    -- Renewing also zeroes used_bytes, so quota is per period.
    renew_period   INTEGER NOT NULL DEFAULT 0,
    -- Operator switch, independent of quota and expiry.
    enabled        INTEGER NOT NULL DEFAULT 1,
    -- Whether the user's credentials are currently installed on nodes. Kept
    -- separate from `enabled` so we can tell "should be off" from "is off",
    -- and only push the difference.
    active         INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_users_sub_token_hash ON users (sub_token_hash);

-- Which access points a user may use. A user gains every node bound to the
-- profile, automatically.
CREATE TABLE user_profiles (
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, profile_id)
);

-- Strong isolation: one credential per user × profile × node, so a leaked
-- credential burns exactly one access point and can be traced and revoked on
-- its own. See docs/user-management.md.
--
-- `email` is Xray's per-user stats key and must be unique across the whole
-- fleet, since that is the name traffic is reported under.
CREATE TABLE credentials (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    node_id    TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    email      TEXT NOT NULL,
    -- The generated secret this credential presents (a UUID for VLESS, a
    -- password for trojan/shadowsocks). Encrypted at rest.
    secret     TEXT NOT NULL,
    -- Traffic attributed to this credential, accumulated from agent deltas.
    up_bytes   INTEGER NOT NULL DEFAULT 0,
    down_bytes INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    UNIQUE (user_id, profile_id, node_id)
);

CREATE UNIQUE INDEX idx_credentials_email ON credentials (email);
CREATE INDEX idx_credentials_node ON credentials (node_id);
CREATE INDEX idx_credentials_user ON credentials (user_id);
