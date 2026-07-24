-- Profiles bundle a server inbound skeleton, the per-user clients[] entry, and
-- one rendering template per client type. See docs/template-system.md §2.
CREATE TABLE profiles (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,
    -- Server inbound skeleton; renders into the node's inbounds array.
    -- Deliberately holds no clients: those come from client_entry.
    inbound_template TEXT NOT NULL DEFAULT '',
    -- One entry of the inbound's clients array, rendered per bound user.
    -- M3's online AddUser/RemoveUser adds and removes exactly this.
    client_entry     TEXT NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);

-- One subscription-rendering template per client type (xray-json, clash, ...).
CREATE TABLE profile_client_templates (
    profile_id TEXT NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    client     TEXT NOT NULL,
    template   TEXT NOT NULL,
    PRIMARY KEY (profile_id, client)
);

-- Which nodes serve a profile. Binding a node is all it takes for every
-- subscriber to gain that node.
CREATE TABLE profile_nodes (
    profile_id TEXT NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    node_id    TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    PRIMARY KEY (profile_id, node_id)
);

-- The variable pool. A variable is a GROUP of components: `xray x25519`
-- yields a private and a public half, and the server and client templates
-- reference the two halves of the same group so they cannot drift apart.
--
-- The owner is modelled as two nullable foreign keys rather than one
-- polymorphic column, so deleting a node or profile cascades to its variables
-- instead of leaving orphans behind.
--
-- User scope arrives with M3, where credentials are minted per
-- user × profile × node.
CREATE TABLE variables (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    scope      TEXT NOT NULL CHECK (scope IN ('global', 'profile', 'node')),
    profile_id TEXT REFERENCES profiles (id) ON DELETE CASCADE,
    node_id    TEXT REFERENCES nodes (id) ON DELETE CASCADE,
    -- Empty for a static value; otherwise the generator that produced it.
    generator  TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK (
        (scope = 'global'  AND profile_id IS NULL     AND node_id IS NULL) OR
        (scope = 'profile' AND profile_id IS NOT NULL AND node_id IS NULL) OR
        (scope = 'node'    AND profile_id IS NULL     AND node_id IS NOT NULL)
    )
);

-- A name is unique within its scope and owner.
CREATE UNIQUE INDEX idx_variables_global ON variables (name) WHERE scope = 'global';
CREATE UNIQUE INDEX idx_variables_profile ON variables (profile_id, name) WHERE scope = 'profile';
CREATE UNIQUE INDEX idx_variables_node ON variables (node_id, name) WHERE scope = 'node';

-- Components of a variable group. `component` is '' for a single-valued
-- variable, so `{{port}}` and `{{reality.private}}` share one storage shape.
-- Secret components are encrypted at rest (see core/internal/secret) and are
-- stripped from client render contexts entirely.
CREATE TABLE variable_components (
    variable_id TEXT NOT NULL REFERENCES variables (id) ON DELETE CASCADE,
    component   TEXT NOT NULL,
    value       TEXT NOT NULL,
    secret      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (variable_id, component)
);

-- A node's config skeleton (log / dns / routing / outbounds). Its inbounds
-- array is filled in from the profiles bound to the node; inbounds written
-- here by hand are preserved and the rendered ones appended.
ALTER TABLE nodes ADD COLUMN config_skeleton TEXT NOT NULL DEFAULT '';
