// Bundles the web UI into dist/ with esbuild.
//   node build.mjs          one-off production build
//   node build.mjs --watch  rebuild on change (run the server with RESTASH_WEB_DIR=web/dist)
//
// JS, CSS and fonts get a content hash in their names, and index.html is
// written to point at them, so caches can never serve an old version.
import * as esbuild from "esbuild";
import { cp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";

const watch = process.argv.includes("--watch");

await rm("dist", { recursive: true, force: true });
await mkdir("dist", { recursive: true });
await writeFile("dist/.gitkeep", "");

const writeHtml = {
  name: "write-html",
  setup(build) {
    build.onEnd(async (result) => {
      if (!result.metafile) return;
      const outputs = Object.entries(result.metafile.outputs);
      const find = (entry) => {
        const hit = outputs.find(([, o]) => o.entryPoint === entry);
        return hit ? "/" + path.basename(hit[0]) : null;
      };
      let html = await readFile("src/index.html", "utf8");
      html = html.replace('href="/styles.css"', `href="${find("src/styles.css")}"`).replace('src="/main.js"', `src="${find("src/main.tsx")}"`);
      await writeFile("dist/index.html", html);
      await cp("src/favicon.svg", "dist/favicon.svg");
    });
  },
};

const options = {
  entryPoints: ["src/main.tsx", "src/styles.css"],
  bundle: true,
  outdir: "dist",
  entryNames: "[name]-[hash]",
  assetNames: "[name]-[hash]",
  loader: { ".woff2": "file", ".woff": "file" },
  metafile: true,
  minify: !watch,
  sourcemap: watch,
  target: "es2022",
  jsx: "automatic",
  logLevel: "info",
  define: { "process.env.NODE_ENV": JSON.stringify(watch ? "development" : "production") },
  plugins: [writeHtml],
};

if (watch) {
  const ctx = await esbuild.context(options);
  await ctx.watch();
} else {
  await esbuild.build(options);
}
