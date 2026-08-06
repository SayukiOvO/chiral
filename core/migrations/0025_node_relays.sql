-- One node of this fleet leaving through another.
--
-- The external relay put somebody else's provider behind one of our nodes.
-- This is the same shape with both ends ours, and it exists for a different
-- reason: not to hide a landing, but to reach one. A subscriber who cannot get
-- a usable connection straight to a distant exit can often reach a nearby
-- entry, and that entry can reach the exit — the classic 中转 arrangement.
--
-- The entry dials the exit with ONE credential, not one per subscriber: an
-- Xray outbound is static, so which connection it carries cannot change what
-- it presents. That credential belongs to no person, which is why it is here
-- and not in `credentials`:
--
--   * `credentials` is keyed by user_id and every row there is somebody's;
--   * accounting must charge a subscriber ONCE, at the entry where they
--     authenticated. If the link's own bytes also landed on a credential, the
--     same gigabyte would be billed twice — once to them, once to nobody.
--
-- So the exit sees this as one more client on the profile named here, and the
-- traffic it accumulates is the link's, not a person's. `AddCredentialTraffic`
-- skips emails it does not know, which is exactly the right behaviour for it.
CREATE TABLE node_relays (
    id      TEXT PRIMARY KEY,
    -- Where subscribers connect.
    entry_node_id TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    -- Where their traffic leaves.
    exit_node_id  TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    -- Which of the exit's access points the entry dials. The entry needs a
    -- client config for the exit, and a profile is exactly that.
    profile_id TEXT NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    -- What subscribers see this line called. The entry's own name is already
    -- taken by its direct line and the exit's by its own, so a relay needs a
    -- name of its own or the document would define two proxies alike.
    label  TEXT NOT NULL,
    -- The link's credential, encrypted at rest like every other secret.
    secret TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    -- Its place in the operator's single ordered list, and what a byte through
    -- it costs — same meaning as everywhere else.
    sort_order   INTEGER NOT NULL DEFAULT 0,
    traffic_rate REAL NOT NULL DEFAULT 1.0,
    created_at INTEGER NOT NULL,
    -- One line per pair per access point. Two would be two identical proxies.
    UNIQUE (entry_node_id, exit_node_id, profile_id)
);

-- A relay is an exit like an external node is, so a subscriber's credential
-- names one or the other. Nullable for the same reason exit_proxy_id is: the
-- foreign key is what cleans up after a deleted relay, and '' matches no row.
ALTER TABLE credentials ADD COLUMN exit_relay_id TEXT REFERENCES node_relays (id) ON DELETE CASCADE;

-- The uniqueness of "one credential per person per access point per exit",
-- restated to include this second kind of exit.
DROP INDEX IF EXISTS idx_credentials_direct;
DROP INDEX IF EXISTS idx_credentials_via_exit;
CREATE UNIQUE INDEX idx_credentials_direct ON credentials (user_id, profile_id, node_id)
    WHERE exit_proxy_id IS NULL AND exit_relay_id IS NULL;
CREATE UNIQUE INDEX idx_credentials_via_proxy ON credentials (user_id, profile_id, node_id, exit_proxy_id)
    WHERE exit_proxy_id IS NOT NULL;
CREATE UNIQUE INDEX idx_credentials_via_relay ON credentials (user_id, profile_id, node_id, exit_relay_id)
    WHERE exit_relay_id IS NOT NULL;
CREATE INDEX idx_credentials_exit_relay ON credentials (exit_relay_id);

-- Who may take a relay line.
--
-- Its own table rather than reusing the entry's or the exit's permissions,
-- because a line is a third thing. "Can reach Tokyo directly", "can reach the
-- box in Tokyo at all" and "can reach Tokyo by way of Hong Kong" are three
-- separate answers, and the whole reason to run a relay is usually that the
-- first is false for somebody the third should be true for.
--
-- Denials, like the other two, so the shape matches — but the API seeds a
-- denial for every existing subscriber when a line is created, so a new line
-- starts belonging to nobody until the operator says otherwise.
CREATE TABLE user_relay_denies (
    user_id  TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    relay_id TEXT NOT NULL REFERENCES node_relays (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, relay_id)
);
