-- D1 (sub_x000000000000041, 2026-10-09): each theme carries a space, the cmux
-- sidebar folder of its subjects' sessions. '' = none yet: its sessions go to
-- « Sans sujet ». Valid names live in spaces.yaml (internal/space), checked by
-- the daemon, not here. Additive.
ALTER TABLE themes ADD COLUMN space TEXT NOT NULL DEFAULT ''
    CHECK (length(space) <= 64 AND instr(space, char(10)) = 0 AND instr(space, char(13)) = 0);
