-- Adds the source discriminator + board-link columns to subjects, so pigtail can
-- hold LOCAL subjects (no board card) and, later, an ingested cache of BOARD
-- subjects. All additive & nullable (source defaults 'local'); pre-existing rows
-- become source='local' with empty board fields — behaviour unchanged otherwise.
ALTER TABLE subjects ADD COLUMN source   TEXT NOT NULL DEFAULT 'local'; -- local|board
ALTER TABLE subjects ADD COLUMN short_id TEXT;   -- board card shortId (ABC-…) when source=board
ALTER TABLE subjects ADD COLUMN board_id TEXT;   -- board UUID when source=board
ALTER TABLE subjects ADD COLUMN section  TEXT;   -- board section/stream id when source=board
