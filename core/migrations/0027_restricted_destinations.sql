-- Networks a node can reach that most subscribers must not.
--
-- The motivating case is DN42: a node peered into it can reach 172.20.0.0/14
-- and *.dn42, and "who may go there" is a different question from "who may
-- use this node" — the operator wants the node fully usable by everyone and
-- the special network usable by almost no one.
--
-- Enforcement is a routing rule on the node itself, matching destination
-- (CIDR / domain suffix) AND the user's email, sending the match to a
-- blackhole. The entry of a connection always knows its destination — the
-- proxy target arrives in the protocol — so no sniffing is required.
--
-- ALLOWS, not denies, unlike every other permission table here. The direction
-- is chosen by what a mistake costs: a node forgotten in a deny table is a
-- node someone can use, which the operator notices and fixes; a special
-- network forgotten in a deny table is a private network exposed to every
-- subscriber, which nobody notices until it has been explored. Default-nobody
-- is the only safe resting state for this one.
CREATE TABLE restricted_destinations (
    id   TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    -- Newline-separated CIDRs and domain suffixes. Two columns rather than a
    -- parsed table: they are rendered into a routing rule as-is, and the
    -- panel validates shape at write time.
    cidrs   TEXT NOT NULL,
    domains TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

-- Which nodes carry the special network, i.e. where the rules are enforced.
-- Scoped per node on purpose (the operator's call): a network like DN42 is
-- peered on specific machines, and rules on machines that cannot reach it
-- anyway would only obscure which node actually enforces something.
--
-- Enforcement also lands on the ENTRY of any relay line whose exit is scoped
-- here — computed at assembly, not stored — because relayed traffic reaches
-- the exit under the line's machine credential, where users can no longer be
-- told apart. The entry is the last point where they can.
CREATE TABLE restricted_destination_nodes (
    dest_id TEXT NOT NULL REFERENCES restricted_destinations (id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    PRIMARY KEY (dest_id, node_id)
);

CREATE TABLE restricted_destination_allows (
    dest_id TEXT NOT NULL REFERENCES restricted_destinations (id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (dest_id, user_id)
);
