// Streams downloads to disk in browsers that can't write files directly.
//
// The page builds a file (a zip of restored files) in pieces. It hands this
// worker a MessagePort and opens /dl/<id>/<name> in a hidden frame; the
// worker answers that request with a stream fed from the port, so the
// browser saves it like any download while the page is still building it.
// Nothing is kept in memory beyond a few MB in flight.
//
// Only requests under /dl/ are touched (that's also the worker's scope).

export {}; // a module, so this file's `self` can be typed as the worker
declare const self: ServiceWorkerGlobalScope;

interface Pending {
  stream: ReadableStream<Uint8Array>;
  headers: Headers;
}

const pending = new Map<string, Pending>();

// How much may wait in the worker before the page is asked to hold on.
const inFlight = 8 << 20;

self.addEventListener("install", () => {
  self.skipWaiting();
});

self.addEventListener("activate", (e) => {
  e.waitUntil(self.clients.claim());
});

/** RFC 6266 filename, with a plain fallback for old browsers. */
function disposition(name: string): string {
  const plain = name.replace(/[^\x20-\x7e]|["\\]/g, "_");
  return `attachment; filename="${plain}"; filename*=UTF-8''${encodeURIComponent(name)}`;
}

self.addEventListener("message", (e) => {
  const msg = e.data as { type?: string; id?: string; name?: string; mime?: string; size?: number };
  // The page pings while a download runs: the data comes over a port, and
  // Firefox doesn't count port messages as activity, so without this it
  // stops the worker (and cuts the download short) after about 30 seconds.
  if (msg?.type === "ping") return;
  const port = e.ports[0];
  if (msg?.type !== "download" || !port || !msg.id || !/^[0-9a-f]{32}$/.test(msg.id)) return;
  const id = msg.id;
  let waiting = false; // the page waits for "more"
  const stream = new ReadableStream<Uint8Array>(
    {
      start(c) {
        port.onmessage = (m) => {
          const d = m.data as { chunk?: ArrayBuffer; end?: boolean; abort?: boolean };
          if (d.chunk) {
            c.enqueue(new Uint8Array(d.chunk));
            if ((c.desiredSize ?? 0) > 0) port.postMessage("more");
            else waiting = true;
          } else if (d.end) {
            c.close();
            pending.delete(id);
          } else if (d.abort) {
            c.error(new Error("cancelled"));
            pending.delete(id);
          }
        };
      },
      pull() {
        if (waiting) {
          waiting = false;
          port.postMessage("more");
        }
      },
      cancel() {
        // The person cancelled the download in the browser.
        port.postMessage("cancelled");
        pending.delete(id);
      },
    },
    new ByteLengthQueuingStrategy({ highWaterMark: inFlight }),
  );
  const headers = new Headers({
    "Content-Type": msg.mime || "application/octet-stream",
    "Content-Disposition": disposition(msg.name || "download"),
    "X-Content-Type-Options": "nosniff",
  });
  if (msg.size && msg.size > 0) headers.set("Content-Length", String(msg.size));
  pending.set(id, { stream, headers });
  port.postMessage("ready");
});

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);
  if (url.origin !== self.location.origin || !url.pathname.startsWith("/dl/")) return;
  const id = url.pathname.split("/")[2] ?? "";
  const p = pending.get(id);
  if (!p) {
    e.respondWith(new Response("This download has ended. Start it again from restash.", { status: 404, headers: { "Content-Type": "text/plain" } }));
    return;
  }
  pending.delete(id);
  e.respondWith(new Response(p.stream, { headers: p.headers }));
});
