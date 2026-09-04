# NodeLane Tunnel web

The website is built with Astro and embedded into the Go server. It keeps the
NodeLane visual foundation separate from Tunnel-specific presentation:

- `src/styles/design-system.css`: reusable NodeLane tokens, header, footer,
  accessibility, and responsive primitives.
- `src/styles/tunnel.css`: Tunnel page layouts and product components.
- `src/i18n/config.ts`: the locale registry, URL paths, text direction, and
  social metadata.
- `src/i18n/locales/`: one fully typed translation file per language. Adding or
  changing a locale fails the type check when required fields are missing.
- `src/components/`: shared layout and interactive page components.
- `src/pages/`: generated localized routes plus `robots.txt` and `sitemap.xml`.

The site ships in Simplified and Traditional Chinese, English, Spanish, French,
German, Japanese, Korean, Brazilian Portuguese, Russian, Arabic, and Hindi.
English is served at `/`, and every other language has a stable, directly
indexable URL. Choosing a language in the header navigates to that version
without automatic browser-language redirects.

## Development

```powershell
pnpm install
pnpm dev
```

## Build

```powershell
pnpm build
```

The production build is copied to `internal/server/assets/web` before the Go
binary is compiled. The Dockerfile and `deploy/package.ps1` perform this step
automatically. Generated output is committed as well so ordinary Go builds and
tests continue to work without requiring Node.js.
