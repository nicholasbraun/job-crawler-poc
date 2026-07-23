-- +goose Up

-- Career-Page Dormancy counters (ADR-0035). consecutive_failures accrues hard-dead
-- probes (a 404 board, or a page that no longer classifies); last_ok stamps the last
-- successful reach. Dormant is DERIVED (consecutive_failures >= the page threshold),
-- never stored, so a threshold change re-derives it for free.
ALTER TABLE career_page ADD COLUMN consecutive_failures int NOT NULL DEFAULT 0;
ALTER TABLE career_page ADD COLUMN last_ok timestamptz;

-- Singleton Collection Crawl (ADR-0036), mirroring the discovery singleton index
-- (migration 0010): a partial unique index on kind over the collection predicate
-- admits at most one crawl_definition of kind 'collection'. Race-proof at the
-- database, unlike an app-level check.
CREATE UNIQUE INDEX crawl_definition_single_collection_idx
    ON crawl_definition (kind) WHERE kind = 'collection';

-- Seed the singleton collection definition with its OWN narrow URL filter
-- (careers/jobs subtrees; blocks the editorial/marketing subtrees Discovery leaves
-- crawlable). seed_urls is empty: a Cycle's seeds come from the whole Catalog at
-- cycle start, not the definition. Migration 0017 already deleted every
-- non-discovery definition, so this insert is clean. The url_filter is the marshaled
-- form of crawler.DefaultCollectionURLFilterConfig(); a testcontainers assertion
-- pins the seeded row to that helper so the two cannot drift.
INSERT INTO crawl_definition (id, name, kind, seed_urls, max_depth, url_filter)
VALUES (
    '00000000-0000-0000-0000-00000c011ec7',
    'collection', 'collection', '{}', 10,
    '{"allowedTLDs":["de","com","org","ai","io","jobs","eu","tech","sh","app","dev","cafe","health","xyz"],"blockedSubdomains":["apps","wiki","foundation","docs","donate","shop","store","marketplace","help","support","forum","community","research","discuss","gist","templates","api","books","cdn","static","assets","status","staging","dev","test","login","auth","sso","accounts","id","ads","mail","email","analytics","tracking","events"],"blockedPathSegments":["add_to","wiki","signin","users","podcast","learning","products","imprint","impressum","contact","privacy","legal","terms","disclaimer","cookie","gdpr","tos","agb","datenschutz","login","signup","register","auth","oauth","sso","account","profile","settings","password","logout","shop","store","cart","checkout","pricing","plans","billing","subscribe","order","help","support","faq","docs","documentation","forum","community","knowledgebase","comments","share","feed","rss","atom","sitemap","social","assets","static","cdn","download","downloads","api","webhook","graphql","landing","promo","campaign","ads","referral","affiliate","events","webinar","top-content","maps","demo","trial","onboarding","tour","features","integrations","changelog","roadmap","status","model","workflows","cgi","cdn-cgi","authenticate","games","wp-content","wp-json","wp-includes","wp-admin","uploads","fileadmin","plugins","themes","category","categories","tag","tags","author","authors","archive","archives","search","blog","news","press","media","articles","article","stories","story","posts","post","magazine"],"blockedHostnames":["www.addtoany.com","trustpilot.com","www.apple.com","x.com","www.x.com","youtube.com","www.youtube.com","youtu.be","www.youtu.be","foundation.wikimedia.com","github.com","www.github.com","tiktok.com","www.tiktok.com","twitter.com","www.twitter.com","roboflow.com","www.roboflow.com","instagram.com","www.instagram.com","google.com","www.google.com","bing.com","www.bing.com","open.spotify"],"blockedFileExtensions":["pdf","doc","docx","xls","xlsx","ppt","pptx","odt","ods","odp","rtf","csv","jpg","jpeg","png","gif","svg","webp","bmp","ico","tif","tiff","zip","rar","gz","tar","7z","bz2","dmg","exe","pkg","mp4","mp3","mov","avi","wmv","m4a","m4v","wav","ogg","webm","flv","mkv","m3u","m3u8","woff","woff2","ttf","otf","eot"],"passSubdomains":["jobs","career","careers","karriere","hiring","recruiting","talent","join","apply","boards","team","job-boards"],"passPathSegments":["jobs","job","careers","career","karriere","vacancies","positions","openings","apply","hiring","opportunities","recruitment","stellenangebote","stellen","team"]}'::jsonb
);

-- +goose Down
DELETE FROM crawl_definition WHERE id = '00000000-0000-0000-0000-00000c011ec7';
DROP INDEX crawl_definition_single_collection_idx;
ALTER TABLE career_page DROP COLUMN last_ok;
ALTER TABLE career_page DROP COLUMN consecutive_failures;
