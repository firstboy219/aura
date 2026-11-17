-- ODI: destinations AURA's control plane talks to (hosts only, no paths or credentials), recorded to decide an allowlist from evidence instead of guessing. Observation only: nothing is blocked.
CREATE TABLE egress_seen (
  host TEXT NOT NULL,
  purpose TEXT NOT NULL,          -- provider | telegram | github
  count INTEGER NOT NULL,
  first_seen INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  PRIMARY KEY (host, purpose));
