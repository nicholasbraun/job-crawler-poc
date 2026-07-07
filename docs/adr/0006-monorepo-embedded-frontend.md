# Monorepo with the React dashboard embedded in the Go binary

The Vite + React + TypeScript dashboard lives in `web/` inside this repo and is compiled
into the Go binary via `embed.FS`, so production ships one self-contained artifact serving
both the API and the dashboard; in development, Vite runs with a proxy to the Go API. The
frontend reads its API base from an env var (default same-origin `/api`), so moving it onto
a separate static host later is a config change, not a rewrite.

## Consequences

The Go build depends on a prior `vite build`, in exchange for single-binary deploys and no
CORS in production. Splitting the frontend out later (A→B) becomes deploy plumbing: stop
embedding, host `dist/` on a CDN, add CORS, point the env-var API base at the API.
