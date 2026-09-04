export const prerender = true;

export function GET(): Response {
  return new Response(
    [
      "User-agent: *",
      "Allow: /",
      "Disallow: /api/",
      "Disallow: /internal/",
      "Sitemap: https://tunnel.nodelane.net/sitemap.xml",
      "",
    ].join("\n"),
    { headers: { "Content-Type": "text/plain; charset=utf-8" } },
  );
}
