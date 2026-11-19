<script lang="ts">
  // Search the folder's contents (git grep on the server): as you type, with case, whole-word and regex
  // switches and an optional file pattern; results grouped by file.
  import { onMount } from "svelte";
  import { api } from "./api";
  import FileIcon from "./FileIcon.svelte";
  import { fade, slide } from "./motion";
  import { CaseSensitive, WholeWord, Regex, ChevronRight, LoaderCircle } from "@lucide/svelte";
  let {
    onopen,
    onerror,
  }: {
    /** A result: the file, its line, and the match in it (column and length, in characters). */
    onopen: (path: string, line: number, col?: number, len?: number) => void;
    onerror: (message: string) => void;
  } = $props();
  type Hit = { path: string; line: number; column: number; content: string };
  const saved = (() => {
    try {
      return JSON.parse(sessionStorage.getItem("falcon.search") || "{}");
    } catch {
      return {};
    }
  })();
  let q = $state<string>(saved.q || ""),
    glob = $state<string>(saved.glob || ""),
    matchCase = $state<boolean>(!!saved.matchCase),
    word = $state<boolean>(!!saved.word),
    regex = $state<boolean>(!!saved.regex),
    hits = $state<Hit[]>([]),
    busy = $state(false),
    problem = $state(""),
    collapsed = $state(new Set<string>()),
    active = $state(0),
    input: HTMLInputElement,
    list: HTMLDivElement;
  let ticket = 0,
    inflight: AbortController | null = null;
  const groups = $derived.by(() => {
    const m = new Map<string, Hit[]>();
    for (const h of hits) m.set(h.path, [...(m.get(h.path) || []), h]);
    return [...m.entries()];
  });
  async function run() {
    const t = ++ticket;
    problem = "";
    if (!q.trim()) {
      hits = [];
      busy = false;
      return;
    }
    busy = true;
    try {
      const params = new URLSearchParams({ q, case: String(matchCase), word: String(word), regex: String(regex), glob });
      // A newer search cancels the one still running (the server stops its git grep).
      inflight?.abort();
      inflight = new AbortController();
      const next = await api<Hit[]>("/api/search?" + params, { signal: inflight.signal });
      if (t === ticket) hits = next;
    } catch (e) {
      if ((e as Error).name === "AbortError") return;
      if (t === ticket) {
        hits = [];
        problem = (e as Error).message;
      }
    } finally {
      if (t === ticket) busy = false;
    }
  }
  $effect(() => {
    // Every input re-runs the search, a moment after typing stops.
    void [q, glob, matchCase, word, regex];
    try {
      sessionStorage.setItem("falcon.search", JSON.stringify({ q, glob, matchCase, word, regex }));
    } catch {}
    const timer = setTimeout(run, 180);
    return () => clearTimeout(timer);
  });
  /** The search as a regular expression, with the server's rules (Match case off means any case). */
  function matcher(): RegExp | null {
    try {
      const body = regex ? q : q.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
      return new RegExp(word ? `(?<![\\p{L}\\p{N}_])(?:${body})(?![\\p{L}\\p{N}_])` : body, "gu" + (matchCase ? "" : "i"));
    } catch {
      return null;
    }
  }
  const re = $derived(q ? matcher() : null);
  /** Where the first match is in a line (characters), and how long. */
  function firstMatch(text: string): { col: number; len: number } | null {
    if (!re) return null;
    re.lastIndex = 0;
    const m = re.exec(text);
    return m && m[0] ? { col: m.index, len: m[0].length } : null;
  }
  /** The line with its matches marked (escaped; the mark is the only markup). A match far along a long
   * line is brought into view: the preview starts just before it. */
  function marked(text: string): string {
    const esc = (s: string) => s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]!);
    let start = text.length - text.replace(/^\s+/, "").length;
    const first = firstMatch(text);
    let lead = "";
    if (first && first.col - start > 28) {
      start = first.col - 16;
      lead = "…";
    }
    const shown = text.slice(start);
    if (!re) return esc(shown);
    let out = lead,
      at = 0;
    re.lastIndex = 0;
    for (const m of shown.matchAll(re)) {
      if (!m[0]) break;
      out += esc(shown.slice(at, m.index)) + "<mark>" + esc(m[0]) + "</mark>";
      at = m.index! + m[0].length;
    }
    return out + esc(shown.slice(at));
  }
  // Results in order as shown (collapsed files skipped): ↑/↓ from the box move through them, Enter opens.
  const flat = $derived(groups.flatMap(([path, list]) => (collapsed.has(path) ? [] : list)));
  $effect(() => {
    void hits;
    active = 0;
  });
  function openHit(h: Hit) {
    const m = firstMatch(h.content);
    onopen(h.path, h.line, m?.col, m?.len);
  }
  function move(by: number) {
    if (!flat.length) return;
    active = Math.max(0, Math.min(flat.length - 1, active + by));
    requestAnimationFrame(() => list?.querySelector(".search-line.active")?.scrollIntoView({ block: "nearest" }));
  }
  function toggle(path: string) {
    const next = new Set(collapsed);
    if (next.has(path)) next.delete(path);
    else next.add(path);
    collapsed = next;
  }
  export function focus() {
    input?.focus();
    input?.select();
  }
  onMount(() => focus());
</script>

<div class="section-label">Search</div>
<div class="search-box">
  <div class="search-input">
    <input
      bind:this={input}
      bind:value={q}
      aria-label="Search the folder"
      placeholder="Search"
      onkeydown={(e) => {
        if (e.key === "Enter" && flat[active]) {
          e.preventDefault();
          openHit(flat[active]);
        } else if (e.key === "ArrowDown") {
          e.preventDefault();
          move(1);
        } else if (e.key === "ArrowUp") {
          e.preventDefault();
          move(-1);
        }
      }}
    />
    <span class="search-toggles">
      <button class:on={matchCase} aria-pressed={matchCase} title="Match case" aria-label="Match case" onclick={() => (matchCase = !matchCase)}><CaseSensitive size={15} /></button>
      <button class:on={word} aria-pressed={word} title="Whole word" aria-label="Whole word" onclick={() => (word = !word)}><WholeWord size={15} /></button>
      <button class:on={regex} aria-pressed={regex} title="Regular expression" aria-label="Regular expression" onclick={() => (regex = !regex)}><Regex size={15} /></button>
    </span>
  </div>
  <input class="search-glob" bind:value={glob} aria-label="Files to include" placeholder="Files to include (e.g. *.go, src/**)" />
  <div class="search-summary" aria-live="polite">
    {#if busy}<LoaderCircle size={12} class="spin" />Searching…{:else if problem}<span class="bad">{problem}</span>{:else if q.trim()}{`${hits.length}${hits.length >= 500 ? "+" : ""} ${hits.length === 1 ? "result" : "results"} in ${groups.length} ${groups.length === 1 ? "file" : "files"}`}{/if}
  </div>
</div>
<div class="search-groups" bind:this={list}>
  {#each groups as [path, list] (path)}<div class="search-group" in:fade={{ duration: 120 }}>
      <button class="search-file" aria-expanded={!collapsed.has(path)} onclick={() => toggle(path)} title={path}
        ><ChevronRight size={12} class={collapsed.has(path) ? "" : "open"} /><FileIcon {path} size={14} /><strong>{path.split("/").at(-1)}</strong><span
          class="search-dir">{path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : ""}</span
        ><em>{list.length}</em></button
      >
      {#if !collapsed.has(path)}<div transition:slide={{ x: 0, y: -4, duration: 140 }}>
          {#each list as h}<button class="search-line" class:active={flat[active] === h} onclick={() => ((active = flat.indexOf(h)), openHit(h))} title={`${h.path}:${h.line}`}
              ><span class="search-ln">{h.line}</span><code>{@html marked(h.content)}</code></button
            >{/each}
        </div>{/if}
    </div>{/each}
</div>
