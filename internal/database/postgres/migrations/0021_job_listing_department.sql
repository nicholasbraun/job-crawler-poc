-- +goose Up
-- Department is the posting's team/department, extracted on the ATS Fetch lane
-- (ADR-0022) and until now discarded at Save. #193 renders it in the SavedSearch
-- panel, so persist it as a display-only Corpus attribute. Deliberately NOT part of
-- search_tsv (search weights title/description/company only, ADR-0037); empty on the
-- crawl lane (the LLM does not emit it). NOT NULL DEFAULT '' — job_listing is empty
-- until the first Collection Cycle, so there is nothing to backfill.
ALTER TABLE job_listing ADD COLUMN department text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE job_listing DROP COLUMN department;
