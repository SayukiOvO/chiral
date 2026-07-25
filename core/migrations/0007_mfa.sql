-- Second factors, email verification, and the short-lived state each needs.

-- An admin's email, and whether it has been proven to reach them. Email is
-- only usable as a factor once verified: sending codes to an unverified
-- address would let a typo lock someone out, or a wrong address hand a factor
-- to a stranger.
ALTER TABLE admins ADD COLUMN email TEXT NOT NULL DEFAULT '';
ALTER TABLE admins ADD COLUMN email_verified INTEGER NOT NULL DEFAULT 0;

-- One row per enrolled factor. An admin may hold several passkeys and one
-- authenticator app at once, so this is a table rather than columns.
CREATE TABLE mfa_credentials (
    id       TEXT PRIMARY KEY,
    admin_id TEXT NOT NULL REFERENCES admins (id) ON DELETE CASCADE,
    kind     TEXT NOT NULL CHECK (kind IN ('totp', 'passkey', 'email')),
    -- Operator-facing label: "iPhone", "YubiKey", "Authenticator app".
    name TEXT NOT NULL,
    -- For totp: the base32 shared secret, encrypted at rest.
    -- For passkey: the serialised credential (public key, id, sign count).
    -- For email: empty; the address lives on the admin.
    secret TEXT NOT NULL DEFAULT '',
    -- Passkeys are looked up by their credential id during assertion.
    credential_id TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER NOT NULL DEFAULT 0,
    -- Enrolment is two-phase: created, then confirmed by proving possession.
    -- An unconfirmed factor never counts towards login.
    confirmed INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_mfa_admin ON mfa_credentials (admin_id);
CREATE UNIQUE INDEX idx_mfa_credential_id ON mfa_credentials (credential_id)
    WHERE credential_id != '';

-- Single-use recovery codes, so losing a phone is not losing the account.
-- Only hashes are stored, like every other credential here.
CREATE TABLE mfa_recovery_codes (
    admin_id   TEXT NOT NULL REFERENCES admins (id) ON DELETE CASCADE,
    code_hash  TEXT NOT NULL,
    used_at    INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (admin_id, code_hash)
);

-- Short-lived state that must not become a bypass: a login that has passed
-- the password but not yet a second factor, a WebAuthn challenge awaiting its
-- response, or an emailed code.
--
-- Kept in one table because they share a lifecycle — issued, used once,
-- expired — and a single sweep can prune them all.
CREATE TABLE auth_challenges (
    token_hash TEXT PRIMARY KEY,
    admin_id   TEXT NOT NULL REFERENCES admins (id) ON DELETE CASCADE,
    -- login: password accepted, second factor pending.
    -- webauthn_register / webauthn_login: a challenge awaiting its response.
    -- email_verify: proving an address reaches its owner.
    -- email_code: a one-time code sent as a second factor.
    purpose TEXT NOT NULL CHECK (purpose IN
        ('login', 'webauthn_register', 'webauthn_login', 'email_verify', 'email_code')),
    -- The WebAuthn session data, or the hash of an emailed code.
    data       TEXT NOT NULL DEFAULT '',
    attempts   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE INDEX idx_auth_challenges_admin ON auth_challenges (admin_id, purpose);
CREATE INDEX idx_auth_challenges_expiry ON auth_challenges (expires_at);
