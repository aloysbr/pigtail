-- Short title shown where the full title does not fit (the cmux sidebar).
-- NULL = derived from the title (pigtail.ShortTitle). Additive, nullable:
-- pre-existing rows gain an empty column and keep the derived label.
ALTER TABLE subjects ADD COLUMN short_title TEXT;
