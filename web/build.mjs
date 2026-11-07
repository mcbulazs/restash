// Bundles the web UI into dist/ with esbuild.
//   node build.mjs          one-off production build
//   node build.mjs --watch  rebuild on change (run the server with RESTASH_WEB_DIR=web/dist)
//
// JS, CSS and fonts get a content hash in their names, and index.html is
// written to point at them, so caches can never serve an old version.
import * as esbuild from "esbuild";
import { access, cp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { gzipSync } from "node:zlib";
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
      html = html.replace("<!-- wasm -->", await copyWasm());
      await writeFile("dist/index.html", html);
      await cp("src/favicon.svg", "dist/favicon.svg");
    });
  },
};

// The restic reader (built by scripts/build-wasm.sh into wasm/) is copied
// with a content hash, plus a gzipped copy the server sends when it can.
// The page finds both through <meta> tags.
async function copyWasm() {
  try {
    await access("wasm/restic.wasm");
  } catch {
    console.warn("wasm/restic.wasm not built: browsing and downloading backups won't work (run scripts/build-wasm.sh)");
    return "";
  }
  const tags = [];
  for (const [file, meta] of [
    ["restic.wasm", "restash-wasm"],
    ["wasm_exec.js", "restash-wasm-exec"],
  ]) {
    const data = await readFile("wasm/" + file);
    const hash = createHash("sha256").update(data).digest("hex").slice(0, 10).toUpperCase();
    const ext = path.extname(file);
    const name = `${path.basename(file, ext)}-${hash}${ext}`;
    await writeFile("dist/" + name, data);
    if (ext === ".wasm") await writeFile(`dist/${name}.gz`, gzipSync(data, { level: 9 }));
    tags.push(`<meta name="${meta}" content="/${name}" />`);
  }
  return tags.join("\n    ");
}

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

// The download helper (a service worker) keeps a fixed name at the site
// root: browsers find it by that URL and check it for updates themselves.
const worker = {
  entryPoints: ["src/sw/sw.ts"],
  bundle: true,
  outfile: "dist/sw.js",
  minify: !watch,
  target: "es2022",
  logLevel: "info",
};

if (watch) {
  const ctx = await esbuild.context(options);
  await ctx.watch();
  await (await esbuild.context(worker)).watch();
} else {
  await esbuild.build(options);
  await esbuild.build(worker);
}
