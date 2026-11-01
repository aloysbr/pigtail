-- sub_x000000000000033 (D5, 2026-10-20; was 0028 on its branch, renumbered after main's 0032): a subject may carry its own space,
-- written by a hand drag of one of its sessions into a space folder; it beats
-- its theme's (D3: focus > subject space > theme space). '' = its theme's.
-- Valid names live in spaces.yaml (internal/space), checked by the daemon. Additive.
ALTER TABLE subjects ADD COLUMN space TEXT NOT NULL DEFAULT ''
    CHECK (length(space) <= 64 AND instr(space, char(10)) = 0 AND instr(space, char(13)) = 0);
