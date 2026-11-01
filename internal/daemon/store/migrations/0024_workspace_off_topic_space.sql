-- D1 (sub_x000000000000019, 2026-10-17): an off-topic declaration carries a
-- space, the cmux sidebar folder its session is filed in. '' = no explicit
-- space: the daemon infers one at read time from a sub_… cited in the reason.
-- Valid names live in spaces.yaml (internal/space), checked by the daemon, not
-- here. Additive.
ALTER TABLE workspace_off_topic ADD COLUMN space TEXT NOT NULL DEFAULT ''
    CHECK (length(space) <= 64 AND instr(space, char(10)) = 0 AND instr(space, char(13)) = 0);
