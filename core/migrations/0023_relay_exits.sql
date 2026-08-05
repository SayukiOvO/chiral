-- Relaying an external node instead of handing it out.
--
-- "经由 <自有节点>" used to mean a client-side chain: the subscriber's own
-- client dialled the provider through our node, using clash's dialer-proxy. It
-- works, but it gives away everything the arrangement was for — the subscriber
-- holds the provider's address and the provider's shared credential, their
-- client has to speak whatever transport the provider chose (Stash cannot do
-- xhttp at all), and none of per-user isolation, traffic accounting or "disable
-- this person" can apply, because the panel is not in the path.
--
-- It now means a server-side relay: the provider becomes an OUTBOUND on our
-- node, the subscriber connects to an inbound of ours with a credential we
-- minted, and routing sends that credential's traffic out through the
-- provider. The subscriber never learns the provider exists. Everything the
-- fleet can do for its own nodes it can now do for somebody else's.
--
-- Which means a credential is no longer one per user × profile × node: the
-- same person on the same inbound needs a separate one per exit, because the
-- routing rule that picks the exit matches on the credential's email and has
-- nothing else to go on. SQLite cannot widen a table-level UNIQUE, so the
-- table is rebuilt — ids and traffic counters carried across verbatim, since
-- they are what accounting has been accumulating into.
CREATE TABLE credentials_new (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    node_id    TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    -- The exit this credential's traffic leaves through. NULL is the node's
    -- own outbound, which is what every credential was until now.
    --
    -- Nullable rather than an empty string, because the foreign key is what
    -- makes deleting a provider take its credentials with it, and '' matches
    -- no row. The cost is that UNIQUE treats NULLs as distinct, so the
    -- uniqueness this table has always had is restated below as two partial
    -- indexes instead of one table constraint.
    exit_proxy_id TEXT REFERENCES external_proxies (id) ON DELETE CASCADE,
    email      TEXT NOT NULL,
    secret     TEXT NOT NULL,
    up_bytes   INTEGER NOT NULL DEFAULT 0,
    down_bytes INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);

INSERT INTO credentials_new
    (id, user_id, profile_id, node_id, exit_proxy_id, email, secret, up_bytes, down_bytes, created_at)
SELECT id, user_id, profile_id, node_id, NULL, email, secret, up_bytes, down_bytes, created_at
FROM credentials;

DROP TABLE credentials;
ALTER TABLE credentials_new RENAME TO credentials;

-- The uniqueness the table constraint used to give, in the only form SQLite
-- offers once one of the columns can be NULL.
CREATE UNIQUE INDEX idx_credentials_direct
    ON credentials (user_id, profile_id, node_id) WHERE exit_proxy_id IS NULL;
CREATE UNIQUE INDEX idx_credentials_via_exit
    ON credentials (user_id, profile_id, node_id, exit_proxy_id) WHERE exit_proxy_id IS NOT NULL;

-- Rebuilding the table drops every index that was on it, including the ones
-- 0003 created. They are recreated here rather than left to chance — and the
-- email one stays UNIQUE, which is not decoration: Xray identifies a user by
-- that string, so two credentials sharing one would have their traffic added
-- together and `xray api rmu` would remove whichever it found first.
CREATE UNIQUE INDEX idx_credentials_email ON credentials (email);
CREATE INDEX idx_credentials_node ON credentials (node_id);
CREATE INDEX idx_credentials_user ON credentials (user_id);
CREATE INDEX idx_credentials_exit ON credentials (exit_proxy_id);

-- A proxy used as an exit is hidden from subscriptions: the whole point is that
-- the subscriber does not get the provider's address. The operator can still
-- put it back — wanting one direct line for themselves is a real thing — but
-- that is a decision, not the default.
ALTER TABLE external_proxies ADD COLUMN relay_exposed INTEGER NOT NULL DEFAULT 0;
