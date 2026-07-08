-- +goose Up
CREATE TABLE company (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_key    text UNIQUE NOT NULL,
    display_domain text,
    name           text,
    first_seen     timestamptz NOT NULL DEFAULT now(),
    last_seen      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE career_page (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id        uuid NOT NULL REFERENCES company (id),
    url               text NOT NULL,
    politeness_domain text,
    first_seen        timestamptz NOT NULL DEFAULT now(),
    last_seen         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE job_listing (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    definition_id uuid NOT NULL REFERENCES crawl_definition (id),
    url           text NOT NULL,
    company_id    uuid REFERENCES company (id),
    company       text,
    title         text NOT NULL,
    description   text,
    location      text,
    remote        boolean,
    tech_stack    text[] NOT NULL DEFAULT '{}',
    content_hash  text,
    first_seen    timestamptz NOT NULL DEFAULT now(),
    last_seen     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (definition_id, url)
);

-- +goose Down
DROP TABLE job_listing;
DROP TABLE career_page;
DROP TABLE company;
