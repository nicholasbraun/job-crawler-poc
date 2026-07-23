-- +goose Up
-- A SavedSearch is a named, stored ListingQuery over the Corpus (ADR-0037): the
-- keyword / country / work-arrangement filter a user defines once and watches as a
-- live dashboard panel. No owner, no notifications in v1. The three facet arrays
-- default to empty (an unset facet means "any"); created_at gives the panels a
-- stable order.
CREATE TABLE saved_search (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name              text NOT NULL,
    keywords          text[] NOT NULL DEFAULT '{}',
    countries         text[] NOT NULL DEFAULT '{}',
    work_arrangements text[] NOT NULL DEFAULT '{}',
    created_at        timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE saved_search;
