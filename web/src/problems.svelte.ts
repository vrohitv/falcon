import { api } from "./api";
import { on } from "./socket.svelte";

// The folder's problems, as every language server running for it reports them, kept current by the
// socket: a server publishing problems makes the server say "problems", and the list is fetched again.
export type FolderProblem = { path: string; line: number; character: number; severity: number; message: string; source?: string; server: string };
export const problems = $state({ list: [] as FolderProblem[], errors: 0, warnings: 0, others: 0 });

let timer: ReturnType<typeof setTimeout> | undefined;
export async function refreshProblems() {
  try {
    const list = await api<FolderProblem[]>("/api/lsp/problems");
    problems.list = list;
    problems.errors = list.filter((p) => p.severity === 1).length;
    problems.warnings = list.filter((p) => p.severity === 2).length;
    problems.others = list.length - problems.errors - problems.warnings;
  } catch {}
}

let off: (() => void) | null = null;
/** Follows the open folder's problems (call once the folder's socket is connected). */
export function followProblems() {
  off?.();
  problems.list = [];
  problems.errors = problems.warnings = problems.others = 0;
  void refreshProblems();
  off = on("problems", () => {
    clearTimeout(timer);
    timer = setTimeout(() => void refreshProblems(), 150);
  });
}
