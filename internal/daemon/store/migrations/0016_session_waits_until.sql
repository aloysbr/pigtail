-- When an automatic wait ends on its own (subject sub_x000000000000032, A2):
-- the usage limit's reset (quotaLimits.resetsAt), epoch ms; NULL = no end
-- of its own. Additive.
ALTER TABLE session_waits ADD COLUMN until_ms INTEGER;
