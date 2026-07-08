/// <reference types="vite/client" />

interface ImportMetaEnv {
  // Overrides the API origin; unset means the SPA and API share an origin (the
  // embedded server) and requests go to a relative /api.
  readonly VITE_API_BASE_URL?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
