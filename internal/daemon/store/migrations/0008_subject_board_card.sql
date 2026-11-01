-- Adds a first-class link from a subject to a board card (its shortId,
-- e.g. ABC-1204). Distinct from the source='board' mirror columns (short_id/
-- board_id/section): board_card is the link a LOCAL/normal subject carries so the
-- `<type>/ABC-<id>-<slug>` branch convention can drive board↔subject auto-sync,
-- instead of stashing the link in a log line or vault frontmatter.
-- Additive, nullable, source-agnostic; pre-existing rows gain an empty column.
ALTER TABLE subjects ADD COLUMN board_card TEXT;   -- linked board card shortId (ABC-…), any source
