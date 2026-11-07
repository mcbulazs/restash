// The restic reader, compiled from Go to WebAssembly (cmd/restash-wasm).
// It fetches a PC's encrypted backup from the server and decrypts it here,
// so file names and contents only ever exist unlocked in this page.

export interface Snapshot {
  id: string;
  time: string;
  tree: string;
  paths: string[];
  hostname: string;
  tags: string[] | null;
  root: string; // where browsing starts
}

export interface Node {
  name: string;
  type: string; // file, dir, symlink...
  mtime: string;
  size?: number;
  path: string; // inside the backup
  original: string; // where it was on the PC
}

type Write = (chunk: Uint8Array) => Promise<void>;

interface Raw {
  open(url: string, password: string): Promise<number>;
  snapshots(h: number): Promise<string>;
  list(h: number, snap: string, dir: string): Promise<string>;
  original(h: number, snap: string, path: string): string;
  size(h: number, snap: string, paths: string[], limit: number): Promise<string>;
  search(h: number, snap: string, dir: string, q: string, max: number): Promise<string>;
  file(h: number, snap: string, path: string, write: Write): Promise<void>;
  zip(h: number, snap: string, base: string, paths: string[], write: Write, progress: (n: number) => void): Promise<void>;
  close(h: number): void;
}

declare global {
  // eslint-disable-next-line no-var
  var restashRestic: Raw | undefined;
  // eslint-disable-next-line no-var
  var Go: new () => { importObject: WebAssembly.Imports; run(i: WebAssembly.Instance): Promise<void> };
}

let loading: Promise<Raw> | null = null;

function meta(name: string): string {
  const v = document.querySelector<HTMLMetaElement>(`meta[name="${name}"]`)?.content;
  if (!v) throw new Error("This server was built without the backup reader.");
  return v;
}

function loadScript(src: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const s = document.createElement("script");
    s.src = src;
    s.onload = () => resolve();
    s.onerror = () => reject(new Error("Couldn't load the backup reader."));
    document.head.appendChild(s);
  });
}

/** Loads the reader once (about 3 MB). */
export function loadReader(): Promise<Raw> {
  loading ??= (async () => {
    await loadScript(meta("restash-wasm-exec"));
    const go = new globalThis.Go();
    const res = await WebAssembly.instantiateStreaming(fetch(meta("restash-wasm")), go.importObject);
    go.run(res.instance); // keeps running in the background
    for (let i = 0; i < 100 && !globalThis.restashRestic; i++) await new Promise((r) => setTimeout(r, 20));
    if (!globalThis.restashRestic) throw new Error("The backup reader didn't start.");
    return globalThis.restashRestic;
  })();
  loading.catch(() => (loading = null));
  return loading;
}

/** An opened backup of one PC. */
export class Backup {
  private constructor(
    private raw: Raw,
    private h: number,
  ) {}

  static async open(pcID: string, password: string): Promise<Backup> {
    const raw = await loadReader();
    const h = await raw.open(`${location.origin}/repo/${pcID}/`, password);
    return new Backup(raw, h);
  }

  async snapshots(): Promise<Snapshot[]> {
    return JSON.parse(await this.raw.snapshots(this.h));
  }
  async list(snap: string, dir: string): Promise<Node[]> {
    return JSON.parse(await this.raw.list(this.h, snap, dir));
  }
  original(snap: string, path: string): string {
    return this.raw.original(this.h, snap, path);
  }
  async size(snap: string, paths: string[], limit = 0): Promise<{ bytes: number; files: number; done: boolean }> {
    return JSON.parse(await this.raw.size(this.h, snap, paths, limit));
  }
  async search(snap: string, dir: string, q: string, max = 100): Promise<string[]> {
    return JSON.parse(await this.raw.search(this.h, snap, dir, q, max));
  }
  file(snap: string, path: string, write: Write): Promise<void> {
    return this.raw.file(this.h, snap, path, write);
  }
  zip(snap: string, base: string, paths: string[], write: Write, progress: (n: number) => void): Promise<void> {
    return this.raw.zip(this.h, snap, base, paths, write, progress);
  }
  close() {
    this.raw.close(this.h);
  }
}

/** Where a download goes: straight to disk where the browser allows it,
 * otherwise collected and handed over at the end. */
export interface Sink {
  write: Write;
  close(): Promise<void>;
  abort(): Promise<void>;
}

export async function saveAs(name: string, type: string): Promise<Sink> {
  const picker = (window as unknown as { showSaveFilePicker?: (o: object) => Promise<FileSystemFileHandle> }).showSaveFilePicker;
  if (picker) {
    const handle = await picker({ suggestedName: name });
    const w = await handle.createWritable();
    return {
      write: (c) => w.write(c as BufferSource),
      close: () => w.close(),
      abort: () => w.abort(),
    };
  }
  const parts: BlobPart[] = [];
  return {
    write: async (c) => {
      parts.push(c as BlobPart);
    },
    close: async () => {
      const url = URL.createObjectURL(new Blob(parts, { type }));
      const a = document.createElement("a");
      a.href = url;
      a.download = name;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 60_000);
    },
    abort: async () => {
      parts.length = 0;
    },
  };
}
