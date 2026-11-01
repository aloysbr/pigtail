-- D5 (sub_x000000000000018, 2026-10-09): a bound cmux workspace always
-- carries its subject's short title; a manual rename is put back by the
-- sidebar loop. A title taken by hand before D5 goes back to pigtail.
-- applied_title and prev_title are kept: the loop puts the short title back
-- at its next pass (one note in the subject's log), and a detach still gives
-- back the name from before the link. Descriptions keep their owner.
UPDATE subject_cmux_bindings SET title_owner = 'pigtail' WHERE title_owner = 'user';
