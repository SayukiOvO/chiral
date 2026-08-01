-- The order subscribers see their nodes in.
--
-- There was none. Fleet nodes went into a subscription sorted by node id —
-- random hex — so the list a customer opened was in an order nobody chose and
-- nobody could predict, and it changed shape every time a node was added.
--
-- One sequence across both tables rather than one per table, because the
-- operator is ordering a single list: an external exit belongs next to the
-- fleet node it is chained through, not in a separate block after it.
--
-- 0 means "never placed". Those sort after everything that has been placed,
-- keeping their old stable order among themselves, so a new node appears at the
-- end instead of somewhere in the middle and nothing has to be renumbered when
-- one arrives.
--
-- This is also what orders the members of every proxy-group: the group
-- generator walks the rendered proxy list and takes the ones its pattern
-- matches, in the order it finds them. So there is exactly one thing to set,
-- and "the order inside 节点选择" is not a second setting that could disagree
-- with the first.
ALTER TABLE nodes ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0;

ALTER TABLE external_proxies ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0;
