<script lang="ts">
  // Go to file (Ctrl+P): fuzzy-match every file in the folder; name:N goes to a line. Starting with # searches
  // every declared name instead (go to symbol, Ctrl+Shift+O), from the folder's symbol index.
  import { untrack } from "svelte";
  import { api } from "./api";
  import FileIcon from "./FileIcon.svelte";
  import { fade, pop } from "./motion";
  let {
    onopen,
    onclose,
    initial = "",
  }: {
    onopen: (path: string, line: number) => void;
    onclose: () => void;
    initial?: string;
  } = $props();
  type Result = { path: string; name: string; pos: number[]; kind?: string; line?: number };
  // The dialog is made fresh each time it opens, so its first text is all that is needed.
  // Opened again while it was still fading out (Svelte then keeps the same box), a new start text
  // (# for symbols) still applies.
  let shownFor = untrack(() => initial);
  $effect(() => {
    if (initial !== shownFor) {
      shownFor = initial;
      q = initial;
    }
  });
  let q = $state(untrack(() => initial)),
    indexing = $state(false),
    results = $state<Result[]>([]),
    active = $state(0),
    list = $state<HTMLElement>();
  let ticket = 0,
    loading = false,
    enterWaiting = false;
  $effect(() => {
    const symbols = q.startsWith("#");
    const query = symbols ? q.slice(1) : q.replace(/:\d*$/, "");
    const t = ++ticket;
    loading = true;
    const timer = setTimeout(async () => {
      try {
        let next: Result[];
        if (symbols) {
          const r = query.trim()
            ? await api<{ ready: boolean; symbols: { name: string; kind: string; path: string; line: number }[] }>("/api/symbols?q=" + encodeURIComponent(query))
            : { ready: true, symbols: [] };
          indexing = !r.ready;
          next = r.symbols.map((s) => ({ path: s.path, name: s.name, pos: [], kind: s.kind, line: s.line }));
        } else next = await api<Result[]>("/api/fuzzy?q=" + encodeURIComponent(query));
        if (t === ticket) {
          results = next || [];
          active = 0;
          loading = false;
          // Enter pressed while typing ahead of the results: open the best match now.
          if (enterWaiting) {
            enterWaiting = false;
            choose(results[0]);
          }
        }
      } catch {
        if (t === ticket) loading = false;
      }
    }, 40);
    return () => clearTimeout(timer);
  });
  $effect(() => {
    void active;
    list?.querySelector(`[data-i="${active}"]`)?.scrollIntoView({ block: "nearest" });
  });
  function choose(r: Result | undefined) {
    if (!r) return;
    const m = q.match(/:(\d+)$/);
    onopen(r.path, r.line ?? (m ? Number(m[1]) : 1));
    onclose();
  }
  /** The path with matched characters marked. */
  function parts(r: Result) {
    const set = new Set(r.pos || []);
    return [...r.path].map((c, i) => ({ c, hit: set.has(i) }));
  }
  function focus(node: HTMLInputElement) {
    node.focus();
  }
</script>

<div class="palette-backdrop" role="presentation" onclick={onclose} transition:fade={{ duration: 120 }}></div>
<div class="palette" role="dialog" aria-label="Go to file" transition:pop={{ duration: 180, start: 0.97 }}>
  <input
    class="palette-input"
    use:focus
    bind:value={q}
    placeholder="Go to file… (name:line for a line, # for a symbol)"
    aria-label="Go to file"
    onkeydown={(e) => {
      if (e.key === "Escape") onclose();
      else if (e.key === "ArrowDown") {
        e.preventDefault();
        active = Math.min(results.length - 1, active + 1);
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        active = Math.max(0, active - 1);
      } else if (e.key === "Enter") {
        e.preventDefault();
        if (loading) enterWaiting = true;
        else choose(results[active]);
      }
    }}
  />
  <div class="palette-results" bind:this={list} role="listbox">
    {#each results as r, i (r.path + ":" + (r.line ?? 0) + r.name)}<button
        data-i={i}
        role="option"
        aria-selected={i === active}
        class:active={i === active}
        onmousemove={() => (active = i)}
        onclick={() => choose(r)}
        >{#if r.kind}<span class="symbol-badge" data-kind={r.kind}>{r.kind.slice(0, 2)}</span><strong>{r.name}</strong><span class="palette-path"
            >{r.path}:{r.line}</span
          >{:else}<FileIcon path={r.path} size={15} /><strong>{r.name}</strong><span class="palette-path"
            >{#each parts(r) as p}{#if p.hit}<b>{p.c}</b>{:else}{p.c}{/if}{/each}</span
          >{/if}</button
      >{/each}
    {#if !results.length}<p class="palette-empty">
        {#if q.startsWith("#")}{indexing ? "Indexing the folder’s symbols…" : q.length > 1 ? "No matching symbols" : "Type a function, type or class name"}{:else}No matching files{/if}
      </p>{/if}
  </div>
  <div class="palette-footer"><span><kbd>↑</kbd><kbd>↓</kbd> choose</span><span><kbd>Enter</kbd> open</span><span><kbd>esc</kbd> close</span></div>
</div>
