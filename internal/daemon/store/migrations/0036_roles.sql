-- Role supervision (L1, feat/role-supervision): a role binds a name to a subject
-- that must act on a cadence; pigtaild's roles sweep judges it. Additive.
CREATE TABLE roles (
	scope      TEXT NOT NULL,
	name       TEXT NOT NULL,
	subject_id TEXT NOT NULL,
	cadence_ms INTEGER NOT NULL,
	grace_ms   INTEGER NOT NULL,
	stall_ms   INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (scope, name)
)
