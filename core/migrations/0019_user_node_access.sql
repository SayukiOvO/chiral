-- Which nodes a particular subscriber may use.
--
-- Entitlement has been per access-configuration until now: grant somebody a
-- profile and they get every node bound to it. That is the right default —
-- profiles exist to describe a way in, not a machine — but it left no way to
-- say "this person, not that box", and an operator who wanted one had to split
-- a profile in two and keep the copies in step by hand.
--
-- Stored as denials rather than grants, which decides what happens to a node
-- nobody has been asked about yet: a new node is usable by everyone the moment
-- it is bound, instead of by nobody until the operator has been round every
-- subscriber. It also means these tables are empty for a fleet that does not
-- need the feature, and every existing subscriber keeps exactly what they had.
CREATE TABLE user_node_denies (
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, node_id)
);

-- The same, for nodes this panel does not run. A separate table because it
-- references a different parent and has to cascade with it: an external source
-- that is deleted must not leave denials pointing at proxies that are gone.
CREATE TABLE user_external_denies (
    user_id  TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    proxy_id TEXT NOT NULL REFERENCES external_proxies (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, proxy_id)
);
