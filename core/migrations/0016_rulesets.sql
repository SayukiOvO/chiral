-- Routing rules for clash-family clients.
--
-- Until now a subscription carried proxies and two groups and no rules at all,
-- so every client routed everything through the proxy — the subscriber's bank,
-- their local printer and their government's websites included. Rules are what
-- makes a subscription usable rather than merely connected.
--
-- The presets are ACL4SSR's, in subconverter's remote-config format. Two kinds
-- of source, one table: a built-in is identified by its key and its URL is
-- filled in from the compiled-in list, a custom one carries whatever URL the
-- operator pasted. Storing both the same way is what makes switching between
-- them a single column change rather than two code paths.
CREATE TABLE rulesets (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    -- The built-in preset key (e.g. "ACL4SSR_Online"), or '' for a custom one.
    preset     TEXT NOT NULL DEFAULT '',
    -- Where the .ini came from. Always set, including for built-ins, so a
    -- refresh does not have to consult the compiled-in table to know.
    url        TEXT NOT NULL,
    -- The fetched .ini, kept so the panel can render subscriptions while
    -- GitHub is unreachable — which, for this software's users, is the normal
    -- case rather than the exception.
    ini        TEXT NOT NULL DEFAULT '',
    fetched_at INTEGER,
    -- The last fetch failure, cleared on success. Shown rather than logged:
    -- a ruleset that silently stopped updating is one that quietly serves
    -- last year's rules.
    last_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX rulesets_name ON rulesets (name);

-- One row per rule list, shared across every ruleset that references it: the
-- ACL4SSR presets overlap heavily, and fetching ChinaDomain.list once per
-- preset would be twenty copies of the same 17 kB.
CREATE TABLE rule_lists (
    url        TEXT PRIMARY KEY,
    body       TEXT NOT NULL,
    -- Kept so a refresh can be conditional; upstream serves these from a CDN
    -- that honours it.
    etag       TEXT NOT NULL DEFAULT '',
    fetched_at INTEGER NOT NULL
);

-- Which ruleset a subscriber gets. NULL means none, which is the old
-- behaviour: proxies and groups, no rules.
ALTER TABLE users ADD COLUMN ruleset_id TEXT REFERENCES rulesets (id) ON DELETE SET NULL;
