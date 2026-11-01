-- 0028_theme_kind_ongoing.sql — D1 (sub_x000000000000010): a theme of kind
-- "area" is a continuous project; "area" now names the level above (the
-- space, Cockpit lot). Data-only, idempotent.
UPDATE themes SET kind = 'ongoing' WHERE kind = 'area';
