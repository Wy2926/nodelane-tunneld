import {
  getLocalePath,
  localeDefinitions,
} from "../i18n";

export const prerender = true;

const site = "https://tunnel.nodelane.net";
const entries = localeDefinitions.map(({ code }) => ({
  locale: code,
  url: new URL(getLocalePath(code), site).toString(),
}));

const alternateLinks = localeDefinitions
  .map(
    ({ code }) =>
      `    <xhtml:link rel="alternate" hreflang="${code}" href="${new URL(getLocalePath(code), site)}" />`,
  )
  .join("\n");

export function GET(): Response {
  const urls = entries
    .map(
      (entry) => `  <url>
    <loc>${entry.url}</loc>
${alternateLinks}
    <xhtml:link rel="alternate" hreflang="x-default" href="https://tunnel.nodelane.net/" />
  </url>`,
    )
    .join("\n");

  return new Response(
    `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">
${urls}
</urlset>
`,
    { headers: { "Content-Type": "application/xml; charset=utf-8" } },
  );
}
