<script lang="ts">
  // A Markdown document, rendered: headings, tables, task lists, code (highlighted), images from the
  // folder, links to other files (opened in Falcon) and to headings (scrolled to).
  import { api, apiURL } from "./api";
  import { renderMarkdown } from "./markdown";
  import { fade } from "./motion";
  let {
    path,
    disk = null,
    onopen,
  }: {
    path: string;
    disk?: { path: string; hash: string; exists: boolean; n: number } | null;
    onopen: (path: string, line: number) => void;
  } = $props();
  let html = $state(""),
    error = $state(""),
    hash = "",
    root = $state<HTMLElement>();
  const dir = $derived(path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "");
  /** A link or image target, relative to this file, as a repository path (null if it leaves the folder). */
  function resolve(href: string): string | null {
    const [target, hashPart] = href.split("#");
    let clean = decodeURIComponent(target.split("?")[0]);
    if (!clean) return null;
    const parts = (clean.startsWith("/") ? clean.slice(1) : (dir ? dir + "/" : "") + clean).split("/");
    const out: string[] = [];
    for (const p of parts) {
      if (p === "" || p === ".") continue;
      if (p === "..") {
        if (!out.length) return null;
        out.pop();
      } else out.push(p);
    }
    clean = out.join("/");
    return clean ? clean + (hashPart ? "#" + hashPart : "") : null;
  }
  const raw = (p: string) => apiURL("/api/raw?path=" + encodeURIComponent(p));
  // Code blocks: the language's extension, for the server's highlighter.
  const ext: Record<string, string> = {
    go: "go", ts: "ts", typescript: "ts", tsx: "tsx", js: "js", javascript: "js", jsx: "jsx", json: "json", py: "py", python: "py",
    rust: "rs", rs: "rs", java: "java", c: "c", cpp: "cpp", "c++": "cpp", sh: "sh", bash: "sh", shell: "sh", zsh: "sh", console: "sh",
    yaml: "yaml", yml: "yaml", toml: "toml", html: "html", css: "css", scss: "scss", sql: "sql", ruby: "rb", rb: "rb", swift: "swift",
    kotlin: "kt", php: "php", diff: "diff", svelte: "svelte", vue: "vue", xml: "xml", dockerfile: "dockerfile", makefile: "makefile", md: "md",
  };
  async function load() {
    try {
      const f = await api<{ content: string; hash: string }>("/api/file?path=" + encodeURIComponent(path));
      hash = f.hash;
      html = renderMarkdown(f.content, { resolve, raw });
      error = "";
      queueMicrotask(highlight);
    } catch (e) {
      error = (e as Error).message;
    }
  }
  async function highlight() {
    if (!root) return;
    for (const pre of root.querySelectorAll<HTMLElement>("pre.md-code[data-lang]")) {
      const e = ext[pre.dataset.lang || ""];
      const code = pre.querySelector("code");
      if (!e || !code || pre.dataset.done) continue;
      pre.dataset.done = "1";
      try {
        const r = await api<{ lines: string[] }>("/api/highlight", { method: "POST", body: JSON.stringify({ path: "block." + e, text: code.textContent || "" }) });
        code.classList.add("code-text");
        code.innerHTML = r.lines.join("\n");
      } catch {}
    }
  }
  $effect(() => {
    void path;
    void load();
  });
  // The file changed on disk: show the new version.
  $effect(() => {
    if (disk && disk.path === path && disk.exists && disk.hash && hash && disk.hash !== hash) void load();
  });
  function click(e: MouseEvent) {
    const a = (e.target as HTMLElement).closest("a");
    if (!a) return;
    if (a.dataset.path) {
      e.preventDefault();
      onopen(a.dataset.path, Number(a.dataset.line) || 1);
    } else if (a.dataset.anchor !== undefined) {
      e.preventDefault();
      root?.querySelector(`#${CSS.escape(a.dataset.anchor)}`)?.scrollIntoView({ behavior: "smooth", block: "start" });
    }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="doc-view" onclick={click}>
  {#if error}<div class="inline-error">{error}</div>{/if}
  {#key html}<article class="markdown markdown-doc" bind:this={root} in:fade={{ duration: 120 }}>{@html html}</article>{/key}
</div>
