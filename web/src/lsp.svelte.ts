import { api } from "./api";

// Language-server status, shared by every LSP pill and every lookup. It is
// fetched again whenever it can have changed: a pill appears, the window
// regains focus, the menu opens, settings change, or a lookup finishes.
export type LspServer = {
  server: string;
  languages: string[];
  extensions: string[];
  installed: boolean;
  install?: string;
  /** How to install it on this system, best first, and where each puts it (Falcon looks there). */
  installs?: { command: string; where: string }[];
  /** Where Falcon found it. */
  path?: string;
  /** Its instances for this folder: one per project root (a folder can hold several projects). */
  instances?: { root: string; state: string; doing?: string; documents: number }[] | null;
  state?: "" | "starting" | "indexing" | "ready" | "failed" | "exited";
  note?: string;
  navigates?: boolean;
  slow?: boolean;
  at?: string;
};
export type LspStatus = { execution: boolean; host_execution: boolean; os?: string; servers: LspServer[]; searched?: string[] };
export type LspState = {
  state: "ready" | "indexing" | "starting" | "failed" | "off" | "missing" | "none";
  server?: LspServer;
};

export const lsp = $state<{ status: LspStatus | null; busy: string }>({ status: null, busy: "" });
let inflight: Promise<LspStatus> | null = null,
  fetchedAt = 0;

export function refreshLsp(): Promise<LspStatus> {
  inflight ??= api<LspStatus>("/api/lsp/status")
    .then((s) => {
      lsp.status = s;
      fetchedAt = Date.now();
      return s;
    })
    .catch(() => lsp.status ?? { execution: false, host_execution: false, servers: [] })
    .finally(() => (inflight = null));
  return inflight;
}

/** The status, refetched when older than maxAge milliseconds. */
export function lspStatus(maxAge = 3000): Promise<LspStatus> {
  if (lsp.status && Date.now() - fetchedAt < maxAge) return Promise.resolve(lsp.status);
  return refreshLsp();
}

export function lspStateFor(path: string, s: LspStatus | null): LspState {
  const ext = "." + (path.split(".").at(-1) || "").toLowerCase();
  const server = s?.servers.find((x) => x.extensions.includes(ext));
  if (!server) return { state: "none" };
  if (!server.installed) return { state: "missing", server };
  if (!s!.execution) return { state: "off", server };
  if (lsp.busy === server.server || server.state === "starting") return { state: "starting", server };
  if (server.state === "failed") return { state: "failed", server };
  if (server.state === "indexing") return { state: "indexing", server };
  return { state: "ready", server };
}

/** Whether to ask the language server: it answers while indexing too (and waits for it where it must). */
export function lspUsable(state: LspState["state"]) {
  return state === "ready" || state === "indexing" || state === "starting";
}

/** Forget the status (the tab moved to another folder). */
export function resetLsp() {
  lsp.status = null;
  lsp.busy = "";
  fetchedAt = 0;
}
