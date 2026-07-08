-- +goose Up
-- job_listing.company_id (added in 0002) was never populated: Save writes only
-- the denormalized `company` text and every read selects that text column, so
-- the FK is dead. Drop it rather than wiring a company upsert into the listing
-- path. Forward-only; the denormalized `company` text remains the source.
ALTER TABLE job_listing DROP COLUMN company_id;

-- +goose Down
ALTER TABLE job_listing ADD COLUMN company_id uuid REFERENCES company (id);
