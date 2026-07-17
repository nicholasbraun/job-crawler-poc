-- +goose Up
-- company_key is the Owner CompanyKey (ADR-0021): the durable link from a keyword
-- Job Listing back to its Catalog Company, set at save from the source URL's Owner.
-- NOT NULL DEFAULT '' mirrors the tech_stack idiom in this table so existing rows
-- and roaming ("" Owner) listings never surface SQL NULL to a string Scan.
ALTER TABLE job_listing ADD COLUMN company_key text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE job_listing DROP COLUMN company_key;
