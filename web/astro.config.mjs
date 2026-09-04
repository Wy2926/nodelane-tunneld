import { defineConfig } from "astro/config";

export default defineConfig({
  site: "https://tunnel.nodelane.net",
  output: "static",
  trailingSlash: "always",
  build: {
    format: "directory",
    assets: "assets",
  },
});
