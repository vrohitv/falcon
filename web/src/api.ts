import { untrack } from "svelte";
import { activity } from "./activity.svelte";

// The API of the folder this tab is on. Every tab can be on a different folder of the one server: the
// folder is in the URL (/r/<id>/…), and calls to /api/… are sent to /api/r/<id>/… for it.
export interface FileEntry {
  path: string;
  size: number;
  language: string;
}
export interface Repo {
  id: string;
  name: string;
  path: string;
  branch: string;
  added: string;
}
export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

let current = "";
/** The folder this tab is on ("" on the start page). */
export function repoID() {
  return current;
}
export function setRepo(id: string) {
  current = id;
}
const global = ["/api/repos", "/api/browse", "/api/login"];
/** The URL for an API path, for this tab's folder. */
export function apiURL(path: string) {
  if (!path.startsWith("/api/") || global.some((g) => path.startsWith(g)) || !current) return path;
  return "/api/r/" + encodeURIComponent(current) + path.slice(4);
}

export async function api<T = any>(path: string, options: RequestInit = {}): Promise<T> {
  // Counted without being read reactively: a request started inside an effect must not make that effect
  // depend on the counter (it would run again on every request).
  untrack(() => activity.begin());
  let response: Response;
  try {
    response = await fetch(apiURL(path), {
      ...options,
      headers: { "Content-Type": "application/json", ...options.headers },
    });
  } finally {
    untrack(() => activity.end());
  }
  if (!response.ok) {
    let message = await response.text();
    try {
      message = JSON.parse(message).error || message;
    } catch {}
    throw new APIError(response.status, response.status === 401 ? "Authentication required" : message);
  }
  return response.json();
}

export function timeAgo(value: string) {
  const sec = Math.max(0, (Date.now() - new Date(value).getTime()) / 1000);
  if (sec < 60) return "just now";
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ago`;
  return new Date(value).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}
