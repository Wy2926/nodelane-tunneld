# NodeLane Tunnel Web

One Astro project provides the public website and the authenticated console.
Current implementation progress and unfinished requirements are recorded only
in [the development handoff](../docs/HANDOFF.md).

## Source Boundaries

- `src/i18n/index.ts`: the only translation entry, `getTranslation(locale)`.
  `types.ts` and the original 12 locale files own public, `console`, and `errors`
  messages; `config.ts` owns locale metadata. Do not add a console dictionary.
- `src/styles/design-system.css`: current tokens and shared controls. Use
  `HeaderFrame.astro` and `LanguageSelect.astro` for common page chrome.
- `src/styles/tunnel.css` and `console.css`: public-page and console layouts,
  respectively, without a second set of common controls or brand rules.
- `src/console/`: API, model, DOM, polling, and error formatting. Messages remain
  in the central locale files, not in these modules.
- `src/lib/`: shared command validation and Shell command generation.

Public pages use `/` for English and localized paths for the other languages.
The console uses `/console/tunnels` with a `lang` query parameter and preserves
the route ID during language changes. Its generated `_shells` are not public
routes: Go authenticates the Session before serving them. An Astro preview
alone does not provide that authentication or the backing API.

## Local Checks

Run from this `web` directory:

```powershell
pnpm install --frozen-lockfile
pnpm test
pnpm test:built
```

`test:built` performs type checking, builds the site, and exercises real built
markup. It does not prove visual appearance, live identity login, or deployment.
For full local API/browser testing, use the opt-in fixture described in the handoff.

## Embedded Output

From the Tunnel repository root, rebuild `web/dist` and synchronize only that
generated directory into `internal/server/assets/web`. Confirm both resolved
paths before any mirror operation; never mirror a repository root. The Dockerfile
performs its own frontend build/copy. Do not edit generated HTML/CSS/JS manually.

After synchronization, run `pnpm --dir web test:embedded`. It compares the two
existing directories byte-for-byte, so a fresh build is a prerequisite, not an
effect of this check. Commit the generated output with its source change.
`.gitattributes` preserves LF for embedded text files on Windows; raster assets
must retain their original bytes.
