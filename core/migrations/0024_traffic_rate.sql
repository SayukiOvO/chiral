-- What a gigabyte through this node costs the subscriber.
--
-- Nodes are not equally expensive. A relay on a metered home line or a
-- provider billed per gigabyte cannot sensibly draw from the same quota at the
-- same rate as a flat-rate box, and the alternative to saying so is giving
-- every subscriber a quota sized for the cheapest node and hoping.
--
-- The multiplier applies to the QUOTA, not to the counters. `credentials`
-- keeps the bytes that actually moved — that is what the agent measured and
-- what an operator needs when a number looks wrong — and `users.used_bytes`,
-- which is what a quota is compared against, accumulates the billed amount.
-- Collapsing the two would answer "what did they use" and "what did they owe"
-- with one number that is neither.
ALTER TABLE nodes ADD COLUMN traffic_rate REAL NOT NULL DEFAULT 1.0;

-- External nodes get their own, because for a relayed exit the traffic really
-- leaves through somebody else's line and that is the line being paid for. A
-- relayed credential bills at its exit's rate; everything else at its node's.
ALTER TABLE external_proxies ADD COLUMN traffic_rate REAL NOT NULL DEFAULT 1.0;
