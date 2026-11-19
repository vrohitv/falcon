import { api } from "./api";

// The browser keeps what it has already fetched from the repository, so with
// real latency between the browser and Falcon, reopening a file, switching tabs
// or expanding a folder renders at once.
//
// Correctness comes from content hashes, not from expiry: source windows and
// file contents are stored under their path *and* hash. A file is shown from
// cache only at the hash last seen for it, and that hash is confirmed in the
// background (the OS file watcher's first event for an open tab, or one small
// stat request). Any newer hash drops the path's entries. Directory listings
// are shown from cache and revalidated on every visit.

export const CHUNK = 1000;
const BUDGET = 24 << 20; // ~24 MB of cached text

export interface CodeWindow {
  start: number;
  total: number;
  max_columns: number;
  language: string;
  hash: string;
  lines: string[];
  raw: string[];
  exact: boolean;
  refine: boolean;
}

type Entry = { value: any; size: number; path: string };
const entries = new Map<string, Entry>(); // insertion order = recency
const latest = new Map<string, string>(); // path → last hash seen
let used = 0;

function touch(key: string) {
  const e = entries.get(key);
  if (e) {
    entries.delete(key);
    entries.set(key, e);
  }
  return e?.value;
}
function store(key: string, path: string, value: any, size: number) {
  const old = entries.get(key);
  if (old) used -= old.size;
  entries.delete(key);
  entries.set(key, { value, size, path });
  used += size;
  for (const [k, e] of entries) {
    if (used <= BUDGET) break;
    entries.delete(k);
    used -= e.size;
  }
}
const textSize = (lines: string[]) => lines.reduce((n, l) => n + l.length * 2, 64);

/** The hash this browser last saw for a path, if any. */

/** Record a path's current hash; entries for any other version are dropped. */
export function observe(path: string, hash: string, exists = true) {
  if (!exists || (latest.has(path) && latest.get(path) !== hash)) forget(path);
  if (exists && hash) latest.set(path, hash);
}
function forget(path: string) {
  latest.delete(path);
  for (const [k, e] of entries)
    if (e.path === path) {
      entries.delete(k);
      used -= e.size;
    }
}

const windowKey = (path: string, hash: string, start: number) =>
  `code|${path}|${hash}|${start}`;

/** A cached source window at the last known hash, if present. */
export function cachedWindow(path: string, start: number): CodeWindow | undefined {
  const hash = latest.get(path);
  return hash ? touch(windowKey(path, hash, start)) : undefined;
}

/** Fetch one source window, answering from cache when the hash is known. */
export async function codeWindow(
  path: string,
  start: number,
  hash = "",
  signal?: AbortSignal,
  fresh = false,
): Promise<CodeWindow> {
  if (hash && !fresh) {
    const hit = touch(windowKey(path, hash, start));
    if (hit) return hit;
  }
  const data = await api<CodeWindow>(
    `/api/code?path=${encodeURIComponent(path)}&start=${start}&count=${CHUNK}${hash ? "&hash=" + hash : ""}`,
    { signal },
  );
  observe(path, data.hash);
  // Interim highlighting is kept too; the viewer upgrades it in place when
  // the exact tokens arrive, and the upgrade replaces this entry.
  store(windowKey(path, data.hash, start), path, data, textSize(data.lines));
  return data;
}

/** Whole-file content for the editor, keyed by hash. */
export function cachedContent(path: string): { hash: string; content: string } | undefined {
  const hash = latest.get(path);
  return hash ? touch(`file|${path}|${hash}`) : undefined;
}
export function rememberContent(path: string, file: { hash: string; content: string }) {
  observe(path, file.hash);
  store(`file|${path}|${file.hash}`, path, { hash: file.hash, content: file.content }, file.content.length * 2 + 64);
}

/** Outline symbols, cached for the version they were computed from. */
export async function outline(path: string) {
  const hash = latest.get(path);
  const hit = hash ? touch(`outline|${path}|${hash}`) : undefined;
  if (hit) return hit;
  const symbols = await api<any[]>("/api/outline?path=" + encodeURIComponent(path));
  if (hash && latest.get(path) === hash) store(`outline|${path}|${hash}`, path, symbols, JSON.stringify(symbols).length * 2);
  return symbols;
}

/** Directory listings: shown from cache, then revalidated by the caller. */
export const cachedDirectory = <T>(path: string): T | undefined => touch(`dir|${path}`);
export function rememberDirectory(path: string, list: unknown[]) {
  store(`dir|${path}`, `dir:${path}`, list, JSON.stringify(list).length * 2);
}

// Hovering a file warms its first window, one request at a time, so a click
// usually finds it already here.
let warming: Promise<unknown> | null = null;
export function prefetch(path: string) {
  if (warming || cachedWindow(path, 0)) return;
  warming = codeWindow(path, 0)
    .catch(() => {})
    .finally(() => (warming = null));
}

/** Forget everything (the tab moved to another folder). */
export function resetCaches() {
  entries.clear();
  latest.clear();
  used = 0;
}
