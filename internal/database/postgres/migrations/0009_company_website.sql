-- +goose Up
-- website records a Company's declared homepage (CONTEXT.md "Website"): known
-- only when imported — Discovery never learns it — and the Keyword Crawl's seed
-- of last resort for a Pageless Company. Nullable and non-unique; the empty
-- string in the domain maps to SQL NULL, matching the ats_provider idiom
-- (ADR-0013/0015). Discovery-created companies leave it NULL; only the import
-- merge path writes it.
ALTER TABLE company ADD COLUMN website text;

-- +goose Down
ALTER TABLE company DROP COLUMN website;
