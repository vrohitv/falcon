import { api } from "./api";
import { lsp, lspStateFor, refreshLsp } from "./lsp.svelte";

// Go to definition. With a language server available for the file, the
// answer is semantic; otherwise, or when the server finds nothing, it comes
// from declaration shapes across the repository (syntax matches).
// column/end: characters on the line (from the language server); text: the line, for previews; external:
// the file's real path when it is outside the folder (path is then an ~ext/ path, opened read-only).
export type Target = { path: string; line: number; column?: number; end?: number; text?: string; external?: string; signature?: string; kind?: string };

// A file outside the folder (~ext/…) is looked at with the language server of the folder file it was
// reached from; this remembers which.
const reachedFrom = new Map<string, string>();
export const isExternal = (path: string) => path.startsWith("~ext/");
/** The folder file an outside file was reached from ("" for a file in the folder). */
export function fromFor(path: string): string {
  return isExternal(path) ? reachedFrom.get(path) || "" : "";
}
function remember(targets: Target[], from: string) {
  const origin = isExternal(from) ? fromFor(from) : from;
  for (const t of targets) if (t.external && origin) reachedFrom.set(t.path, origin);
}
export type LspMethod =
  | "textDocument/definition"
  | "textDocument/references"
  | "textDocument/implementation"
  | "textDocument/typeDefinition";
export { lspStatus, lspStateFor, lspUsable, refreshLsp } from "./lsp.svelte";
export type { LspServer, LspStatus, LspState } from "./lsp.svelte";

const syntax = new Map<string, Promise<Target[]>>();
export function syntaxDefinitions(
  name: string,
  from: string,
  qualifier = "",
  line = 0,
): Promise<Target[]> {
  const key = [from, name, qualifier, line].join("\u0000");
  if (!syntax.has(key)) {
    if (syntax.size > 200) syntax.clear();
    syntax.set(
      key,
      api<Target[]>(
        `/api/definition?name=${encodeURIComponent(name)}&from=${encodeURIComponent(from)}` +
          `&qualifier=${encodeURIComponent(qualifier)}&line=${line}`,
      ).catch(() => {
        syntax.delete(key);
        return [];
      }),
    );
  }
  return syntax.get(key)!;
}
/** Forget cached answers, e.g. after a save changed declarations. */
export function forgetDefinitions() {
  syntax.clear();
}

export async function definitions(q: {
  path: string;
  name: string;
  qualifier?: string; // the "pkg" of pkg.Name
  line: number; // 0-based, for the language server
  character: number; // UTF-16 column
  hash: string;
  semantic: boolean;
  method?: LspMethod;
}): Promise<{ targets: Target[]; semantic: boolean }> {
  if (q.semantic && q.hash) {
    // The pill shows "starting" while the server works, then its outcome.
    lsp.busy = lspStateFor(q.path, lsp.status).server?.server || "";
    try {
      const reply = await api<{ locations: Target[] | null }>("/api/lsp", {
        method: "POST",
        body: JSON.stringify({
          path: q.path,
          from: fromFor(q.path),
          method: q.method || "textDocument/definition",
          line: q.line,
          character: q.character,
          expected_hash: q.hash,
        }),
      });
      if (reply.locations?.length) {
        remember(reply.locations, q.path);
        return { targets: reply.locations, semantic: true };
      }
    } catch {
    } finally {
      lsp.busy = "";
      void refreshLsp();
    }
  }
  if (q.method && q.method !== "textDocument/definition") return { targets: [], semantic: false };
  return {
    targets: await syntaxDefinitions(q.name, q.path, q.qualifier, q.line + 1),
    semantic: false,
  };
}

/** The identifier around a column of a line, if any. */
export function wordAt(
  text: string,
  col: number,
): { word: string; start: number; end: number; qualifier: string } | null {
  const ident = /[A-Za-z0-9_$]/;
  let start = col,
    end = col;
  while (start > 0 && ident.test(text[start - 1])) start--;
  while (end < text.length && ident.test(text[end])) end++;
  const word = text.slice(start, end);
  if (!/^[A-Za-z_$][\w$]*$/.test(word)) return null;
  let q = start - 1;
  if (text[q] === ".") while (q > 0 && ident.test(text[q - 1])) q--;
  const qualifier = text[start - 1] === "." ? text.slice(q, start - 1) : "";
  return { word, start, end, qualifier };
}

/** What the language server says about the name at a position (signature, docs), as Markdown. */
export async function hoverAt(path: string, line: number, character: number, hash: string): Promise<string> {
  try {
    const reply = await api<{ hover?: string }>("/api/lsp", {
      method: "POST",
      body: JSON.stringify({ path, from: fromFor(path), method: "textDocument/hover", line, character, expected_hash: hash }),
    });
    return (reply.hover || "").trim();
  } catch {
    return "";
  }
}

export type Problem = {
  line: number; // 1-based
  character: number;
  end_line: number;
  end_character: number;
  severity: "error" | "warning" | "information" | "hint";
  message: string;
  source?: string;
  code?: string | number | null;
};

/** The problems the language server reports for a file (errors, warnings, hints). */
export async function problemsFor(path: string): Promise<{ problems: Problem[]; hash: string; indexing: boolean }> {
  const reply = await api<{ diagnostics: Problem[]; hash: string; indexing: boolean }>("/api/lsp/diagnostics", {
    method: "POST",
    body: JSON.stringify({ path, from: fromFor(path) }),
  });
  return { problems: reply.diagnostics || [], hash: reply.hash, indexing: reply.indexing };
}

// The language features beyond navigation. Each asks the file's language server through /api/lsp.
async function ask<T>(q: Record<string, unknown> & { path: string }): Promise<T> {
  return api<T>("/api/lsp", { method: "POST", body: JSON.stringify({ from: fromFor(q.path), ...q }) });
}

/** A place where the name at the caret appears in its file (kind 1 text, 2 read, 3 write). */
export type Occurrence = { line: number; start: number; end: number; kind: number };
export async function occurrencesAt(path: string, line: number, character: number, hash: string): Promise<Occurrence[]> {
  const r = await ask<{ highlights?: Occurrence[] }>({ path, method: "textDocument/documentHighlight", line, character, expected_hash: hash });
  return r.highlights || [];
}

/** One function in a call hierarchy; item is the server's own, to ask for its callers or callees. */
export type CallItem = { name: string; kind: number; detail?: string; path: string; line: number; external?: string; item: unknown };
export type Call = { item: CallItem; lines: number[] };
export async function prepareCalls(path: string, line: number, character: number, hash: string): Promise<CallItem[]> {
  const r = await ask<{ items?: CallItem[] }>({ path, method: "textDocument/prepareCallHierarchy", line, character, expected_hash: hash });
  return r.items || [];
}
export async function callsOf(path: string, item: CallItem, direction: "incoming" | "outgoing"): Promise<Call[]> {
  const r = await ask<{ calls?: Call[] }>({ path, method: `callHierarchy/${direction}Calls`, item: item.item });
  for (const c of r.calls || []) if (c.item.external) remember([{ path: c.item.path, line: c.item.line, external: c.item.external }], path);
  return r.calls || [];
}

/** Where a rename applies and the current name; null when nothing can be renamed there. */
export async function prepareRename(path: string, line: number, character: number, hash: string): Promise<{ line: number; start: number; end: number; placeholder: string; default?: boolean } | null> {
  const r = await ask<{ rename?: { line: number; start: number; end: number; placeholder: string; default?: boolean } | null }>({ path, method: "textDocument/prepareRename", line, character, expected_hash: hash });
  return r.rename ?? null;
}
export type EditPreview = { path: string; changes: { line: number; before: string; after: string }[]; created?: boolean; renamed?: string };
export async function renameSymbol(path: string, line: number, character: number, hash: string, newName: string): Promise<{ preview: EditPreview[]; token: string }> {
  return ask({ path, method: "textDocument/rename", line, character, expected_hash: hash, new_name: newName });
}

export type CodeAction = { title: string; kind: string; preferred: boolean; token: string };
export async function codeActions(path: string, line: number, character: number, endLine: number, endCharacter: number, hash: string): Promise<CodeAction[]> {
  const r = await ask<{ actions?: CodeAction[] }>({ path, method: "textDocument/codeAction", line, character, end_line: endLine, end_character: endCharacter, expected_hash: hash });
  return r.actions || [];
}

/** Applies a previewed rename or a code action: the files it changed. */
export async function applyChange(token: string): Promise<string[]> {
  const r = await api<{ changed: string[] }>("/api/lsp/apply", { method: "POST", body: JSON.stringify({ token }) });
  return r.changed || [];
}
