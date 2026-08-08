-- +goose Up

-- Reconcile job_listing.career_page_id's FK with the rule the Catalog Doctor
-- actually enforces, and with the live database.
--
-- Migration 0017 declared this FK ON DELETE SET NULL, reasoning that deleting a
-- Career Page should orphan its Corpus listings rather than FK-violate. The live
-- database has plain NO ACTION -- it was migrated with an earlier revision of that
-- file -- so source and production have disagreed ever since, and no test could
-- catch it: testcontainers builds a fresh database from the current files, so the
-- suite only ever sees the declared behaviour.
--
-- NO ACTION is the rule we keep, so this statement is a no-op against the live
-- database and corrective for every fresh one. SET NULL is wrong in both
-- directions the Doctor deletes a page:
--
--   * A rejected page (Aggregator host, ATS posting) was never a valid Career
--     Page, so its crawl-lane listings are mis-attributed by construction --
--     orphaning keeps them visible in the Corpus forever rather than removing
--     them.
--   * A merged-away duplicate's listings are VALID and belong to the surviving
--     row, so they must follow it, not be cut loose.
--
-- Orphaning is the worst outcome for both, because CorpusRepository.ListOpen
-- selects BY career_page_id: a crawl-lane listing with a NULL page can never be
-- reached by the refetch lane again, so it is never re-verified and never closed.
-- It stays Open in the Corpus permanently. (A NULL career_page_id remains legal
-- and normal for the ATS Fetch lane, which has no page to point at.)
--
-- Keeping the FK strict makes it a real integrity guard: any delete path that has
-- not decided what its listings deserve fails loudly instead of silently orphaning
-- them. That is not hypothetical -- this constraint is what stopped the Doctor from
-- destroying four populated softgarden tenant boards misread as postings.
ALTER TABLE job_listing DROP CONSTRAINT job_listing_career_page_id_fkey;
ALTER TABLE job_listing ADD CONSTRAINT job_listing_career_page_id_fkey
    FOREIGN KEY (career_page_id) REFERENCES career_page (id);

-- +goose Down
ALTER TABLE job_listing DROP CONSTRAINT job_listing_career_page_id_fkey;
ALTER TABLE job_listing ADD CONSTRAINT job_listing_career_page_id_fkey
    FOREIGN KEY (career_page_id) REFERENCES career_page (id) ON DELETE SET NULL;
