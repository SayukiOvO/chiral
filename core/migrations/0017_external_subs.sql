-- Nodes that belong to somebody else.
--
-- A provider hands over a link and nothing more: no agent can be installed, so
-- none of the fleet's machinery applies. One credential is shared by every
-- subscriber, there is no per-user isolation, no traffic is counted, and
-- disabling a user does not stop them using it. Those are properties of the
-- arrangement, not gaps to fill in later, and the console says so where an
-- operator adds one.
--
-- What the panel does add is chaining: an external node can be reached through
-- one of this fleet's own, so the provider sees the operator's node rather
-- than the subscriber.
CREATE TABLE external_subs (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    -- Where to fetch. Empty when the operator pasted the body instead, which
    -- is the case for a single node somebody sent over chat.
    url        TEXT NOT NULL DEFAULT '',
    -- The last body, fetched or pasted. Rendering reads this and never the
    -- network: a subscriber pulling their config must not wait on somebody
    -- else's provider, or fail when it is down.
    body       TEXT NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 1,
    fetched_at INTEGER,
    last_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX external_subs_name ON external_subs (name);

-- One row per proxy, rebuilt on every refresh — but the operator's settings on
-- it are carried across.
--
-- Matching a refreshed proxy to its previous row is by name first and by
-- endpoint second. Providers rename constantly, usually to put remaining
-- traffic or an expiry date in the label, and a chain setting that silently
-- detached every time the label changed would be worse than not having one.
CREATE TABLE external_proxies (
    id      TEXT PRIMARY KEY,
    sub_id  TEXT NOT NULL REFERENCES external_subs (id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    type    TEXT NOT NULL,
    server  TEXT NOT NULL,
    port    INTEGER NOT NULL,
    -- The proxy as YAML, ready to emit as one item of a clash proxies list.
    config  TEXT NOT NULL,
    -- The fleet node this one is dialled through, or NULL for a direct dial.
    chain_node_id TEXT REFERENCES nodes (id) ON DELETE SET NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    -- Position in the source, so the subscription lists them in the order the
    -- provider did rather than in whatever order the database returns.
    ord     INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX external_proxies_sub ON external_proxies (sub_id, ord);
