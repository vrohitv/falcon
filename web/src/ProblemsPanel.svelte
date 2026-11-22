<script lang="ts">
  // Problems (Ctrl+Shift+M): every error, warning and hint the folder's language servers report, by file,
  // as in VS Code's Problems view. A language server reports on the files it has looked at.
  import { ChevronRight, CircleX, TriangleAlert, Info } from "@lucide/svelte";
  import FileIcon from "./FileIcon.svelte";
  import { problems, refreshProblems } from "./problems.svelte";
  let { onopen }: { onopen: (path: string, line: number, col: number) => void } = $props();
  let filter = $state("");
  let closed = $state(new Set<string>());
  $effect(() => {
    void refreshProblems();
  });
  const groups = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    const by = new Map<string, typeof problems.list>();
    for (const p of problems.list) {
      if (q && !p.message.toLowerCase().includes(q) && !p.path.toLowerCase().includes(q)) continue;
      by.set(p.path, [...(by.get(p.path) || []), p]);
    }
    return [...by].sort((a, b) => Math.min(...a[1].map((p) => p.severity)) - Math.min(...b[1].map((p) => p.severity)) || a[0].localeCompare(b[0]));
  });
  function toggle(path: string) {
    const next = new Set(closed);
    if (next.has(path)) next.delete(path);
    else next.add(path);
    closed = next;
  }
</script>

<div class="section-label">Problems<span class="problem-totals">{problems.errors} errors · {problems.warnings} warnings</span></div>
<div class="problems-filter"><input class="input" placeholder="Filter problems…" bind:value={filter} aria-label="Filter problems" /></div>
<div class="problems-list">
  {#each groups as [path, list] (path)}<button class="problem-file" aria-expanded={!closed.has(path)} onclick={() => toggle(path)}
      ><span class="twist" class:open={!closed.has(path)}><ChevronRight size={12} /></span><FileIcon {path} size={14} /><b>{path.split("/").at(-1)}</b><span class="dir">{path.split("/").slice(0, -1).join("/")}</span><em>{list.length}</em></button
    >
    {#if !closed.has(path)}{#each list as p}<button class="problem" onclick={() => onopen(p.path, p.line, p.character)} title={p.message}
          >{#if p.severity === 1}<CircleX size={13} class="sev-error" />{:else if p.severity === 2}<TriangleAlert size={13} class="sev-warning" />{:else}<Info size={13} class="sev-info" />{/if}<span class="msg">{p.message}</span><span class="where">{p.source || p.server} [{p.line}, {p.character + 1}]</span></button
        >{/each}{/if}{/each}
  {#if !groups.length}<p class="problems-empty">
      {problems.list.length ? "No problems match the filter." : "No problems have been reported. Language servers report on the files they have looked at; open a file to start one."}
    </p>{/if}
</div>

<style>
  .problem-totals {
    margin-left: auto;
    color: var(--subtle);
    font-weight: 400;
    text-transform: none;
    letter-spacing: 0;
  }
  .problems-filter {
    padding: 0 10px 6px;
  }
  .problems-filter input {
    width: 100%;
  }
  .problems-list {
    overflow: auto;
    flex: 1;
    min-height: 0;
    font-size: 12px;
  }
  .problem-file,
  .problem {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    min-height: 22px;
    padding: 1px 10px;
    border: 0;
    background: none;
    color: var(--text);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .problem-file:hover,
  .problem:hover {
    background: var(--hover-wash);
  }
  .problem-file b {
    color: var(--bright);
    font-weight: 600;
  }
  .problem-file .dir {
    overflow: hidden;
    color: var(--subtle);
    font-size: 11px;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .problem-file em {
    margin-left: auto;
    padding: 0 6px;
    border-radius: 8px;
    background: var(--surface-hover);
    color: var(--muted);
    font-size: 10px;
    font-style: normal;
  }
  .twist {
    display: grid;
    transition: transform 0.12s var(--ease-out);
  }
  .twist.open {
    transform: rotate(90deg);
  }
  .problem {
    padding-left: 30px;
  }
  .problem .msg {
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .problem .where {
    flex-shrink: 0;
    margin-left: auto;
    color: var(--subtle);
    font-size: 11px;
  }
  .problem :global(.sev-error) {
    flex-shrink: 0;
    color: var(--danger);
  }
  .problem :global(.sev-warning) {
    flex-shrink: 0;
    color: var(--warning);
  }
  .problem :global(.sev-info) {
    flex-shrink: 0;
    color: var(--info, var(--accent));
  }
  .problems-empty {
    padding: 8px 14px;
    color: var(--subtle);
    line-height: 1.5;
  }
</style>
