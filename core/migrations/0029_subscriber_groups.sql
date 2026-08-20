-- Subscriber groups: say once what a class of subscriber gets.
--
-- Access has been decided per subscriber: a profile grant plus three denial
-- tables, one row per person per object. That is correct and it does not
-- scale — a new node means one deliberate decision repeated N times, and a
-- decision repeated N times is one that gets made by a loop instead of by a
-- person. The mistake this feature exists to prevent is not "the operator
-- forgot somebody", it is "the operator granted everybody because granting
-- them one at a time was tedious".
--
-- Membership is SINGLE. A subscriber belongs to at most one group, and
-- group_id NULL means the behaviour this panel had before groups existed.
-- Many-to-many was the obvious alternative and it cannot express a denial:
-- with permissions unioned across groups, one group allowing a node overrides
-- every other group denying it, so the deny tables — which is what node
-- control here IS — would quietly stop meaning anything.
--
-- Resolution order is: the subscriber's own row, then their group, then the
-- default. Each object is therefore three-state — no row means inherit — which
-- is why this migration adds the mirror of every existing table: the group
-- needs the same shape the subscriber has, and the subscriber needs a way to
-- say the opposite of what their group says.
CREATE TABLE subscriber_groups (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    note       TEXT NOT NULL DEFAULT '',
    -- The group's routing rules, applied to a member who has not chosen their
    -- own. NULL means the group states nothing, not "no rules".
    ruleset_id TEXT REFERENCES rulesets (id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL
);

ALTER TABLE users ADD COLUMN group_id TEXT REFERENCES subscriber_groups (id) ON DELETE SET NULL;

-- "This subscriber has no routing rules, whatever their group says." Without
-- it, NULL would have to mean both "inherit" and "none", and a member could
-- not be excused from their group's rule set at all. Existing rows get 0,
-- which with group_id NULL resolves to exactly what they have today.
ALTER TABLE users ADD COLUMN ruleset_none INTEGER NOT NULL DEFAULT 0;

-- What the group grants. The mirror of user_profiles.
CREATE TABLE group_profiles (
    group_id   TEXT NOT NULL REFERENCES subscriber_groups (id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, profile_id)
);

-- What the group withholds. The mirrors of user_node_denies,
-- user_relay_denies and user_external_denies.
--
-- A group is created denying every node, line and external proxy that already
-- exists, and every node created later is denied to every group that already
-- exists. Both halves are required and for the same reason the per-subscriber
-- tables work this way: the resting state of an object nobody has ruled on is
-- "not handed out". Leave either half open and the first thing groups would do
-- is hand a new node to a whole class of subscribers the moment it is bound.
CREATE TABLE group_node_denies (
    group_id TEXT NOT NULL REFERENCES subscriber_groups (id) ON DELETE CASCADE,
    node_id  TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, node_id)
);

CREATE TABLE group_relay_denies (
    group_id TEXT NOT NULL REFERENCES subscriber_groups (id) ON DELETE CASCADE,
    relay_id TEXT NOT NULL REFERENCES node_relays (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, relay_id)
);

CREATE TABLE group_external_denies (
    group_id TEXT NOT NULL REFERENCES subscriber_groups (id) ON DELETE CASCADE,
    proxy_id TEXT NOT NULL REFERENCES external_proxies (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, proxy_id)
);

-- The subscriber's exceptions in the direction their existing tables cannot
-- express: a profile their group grants but they must not have, and an object
-- their group withholds but they may use. Together with the tables that
-- already exist, every (subscriber, object) pair can now say grant, deny, or
-- nothing at all.
CREATE TABLE user_profile_denies (
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, profile_id)
);

CREATE TABLE user_node_allows (
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, node_id)
);

CREATE TABLE user_relay_allows (
    user_id  TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    relay_id TEXT NOT NULL REFERENCES node_relays (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, relay_id)
);

CREATE TABLE user_external_allows (
    user_id  TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    proxy_id TEXT NOT NULL REFERENCES external_proxies (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, proxy_id)
);

-- A restricted destination may now admit a group as well as a person, and the
-- two are unioned. The direction is unchanged and stays the exception it has
-- always been: this table lists who IS admitted, and a destination naming
-- nobody admits nobody.
CREATE TABLE restricted_destination_group_allows (
    dest_id  TEXT NOT NULL REFERENCES restricted_destinations (id) ON DELETE CASCADE,
    group_id TEXT NOT NULL REFERENCES subscriber_groups (id) ON DELETE CASCADE,
    PRIMARY KEY (dest_id, group_id)
);
