import { api } from "./api";
import { lsp, lspStateFor, refreshLsp } from "./lsp.svelte";

// Go to definition. With a language server available for the file, the
// answer is semantic; otherwise, or when the server finds nothing, it comes
// from declaration shapes across the repository (syntax matches).
export type Target = { path: string; line: number; signature?: string; kind?: string };
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
          method: q.method || "textDocument/definition",
          line: q.line,
          character: q.character,
          expected_hash: q.hash,
        }),
      });
      if (reply.locations?.length) return { targets: reply.locations, semantic: true };
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
      body: JSON.stringify({ path, method: "textDocument/hover", line, character, expected_hash: hash }),
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
    body: JSON.stringify({ path }),
  });
  return { problems: reply.diagnostics || [], hash: reply.hash, indexing: reply.indexing };
}
