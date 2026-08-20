-- Where a node sends particular traffic, instead of straight out of itself.
--
-- "Netflix leaves through the Japanese home line, everything else goes
-- direct" — a per-node routing decision that has nothing to do with which
-- subscriber is asking. Restricted destinations answer "who may go there";
-- these answer "which way does this go", and the two are deliberately
-- separate tables: one is a permission with an allowlist, the other is
-- plumbing that applies to everyone on the node.
--
-- The landing is one of three, and all three already exist elsewhere in this
-- panel: the node's own egress (direct), an external provider (the same
-- outbound "经由" builds), or another node of this fleet (the same outbound a
-- relay line dials with). Only the third needs anything new — a credential to
-- dial with — and it is stored here for the same reason a relay's is stored
-- on the line: an Xray outbound is static, so what it presents cannot depend
-- on whose connection it carries. It belongs to no subscriber, so it is not
-- in `credentials`, and nothing bills it: the bytes were already charged to
-- whoever authenticated at this node.
CREATE TABLE node_egress_rules (
    id      TEXT PRIMARY KEY,
    -- The node whose config carries this rule.
    node_id TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    label   TEXT NOT NULL,

    -- What to match. Both may be set; they render as TWO Xray rules sharing
    -- one outboundTag, because conditions inside a single rule are AND-ed and
    -- a rule naming both would match nothing at all.
    --   domains: geosite:netflix, or a bare suffix like netflix.com
    --   ips:     geoip:jp, or a CIDR like 1.2.3.0/24
    domains TEXT NOT NULL DEFAULT '',
    ips     TEXT NOT NULL DEFAULT '',

    -- Where it goes: 'direct' | 'external' | 'node'.
    target_kind TEXT NOT NULL CHECK (target_kind IN ('direct', 'external', 'node')),
    -- Set for 'external': somebody else's proxy, translated to an outbound.
    target_proxy_id TEXT REFERENCES external_proxies (id) ON DELETE CASCADE,
    -- Set for 'node': another node of this fleet, plus which of its access
    -- points to dial and the machine credential to present.
    target_node_id    TEXT REFERENCES nodes (id) ON DELETE CASCADE,
    target_profile_id TEXT REFERENCES profiles (id) ON DELETE CASCADE,
    secret            TEXT NOT NULL DEFAULT '',

    enabled    INTEGER NOT NULL DEFAULT 1,
    -- Order is priority: routing is first-match, and the console lets the
    -- operator drag these, so the position is the whole meaning.
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,

    CHECK (
        (target_kind = 'direct'   AND target_proxy_id IS NULL AND target_node_id IS NULL) OR
        (target_kind = 'external' AND target_proxy_id IS NOT NULL AND target_node_id IS NULL) OR
        (target_kind = 'node'     AND target_proxy_id IS NULL AND target_node_id IS NOT NULL
                                  AND target_profile_id IS NOT NULL)
    ),
    -- A rule that matches nothing is a rule that does nothing, and it would
    -- render as an Xray rule with no destination condition — which matches
    -- EVERYTHING. Same trap as an empty user list; refused at the door.
    CHECK (domains <> '' OR ips <> '')
);

CREATE INDEX idx_egress_node ON node_egress_rules (node_id);
CREATE INDEX idx_egress_target_node ON node_egress_rules (target_node_id);
