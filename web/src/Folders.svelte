<script lang="ts">
  // The folder switcher (⌘K): every folder this server serves. Enter switches this tab; ⌘Enter (or the
  // arrow) opens it in a new tab, so several folders can be open side by side.
  import type { Repo } from "./api";
  import { fade, pop } from "./motion";
  import { FolderGit2, ExternalLink, Plus, X, GitBranch, Check } from "@lucide/svelte";
  let {
    repos,
    current,
    onswitch,
    onadd,
    onremove,
    onclose,
  }: {
    repos: Repo[];
    current: string;
    onswitch: (id: string, newTab: boolean) => void;
    onadd: () => void;
    onremove: (repo: Repo) => void;
    onclose: () => void;
  } = $props();
  let q = $state(""),
    active = $state(0);
  const shown = $derived(
    repos.filter((r) => !q.trim() || (r.name + " " + r.path).toLowerCase().includes(q.trim().toLowerCase())),
  );
  $effect(() => {
    void q;
    active = 0;
  });
  function focus(node: HTMLInputElement) {
    node.focus();
  }
  const home = (p: string) => p.replace(/^\/(Users|home)\/[^/]+/, "~").replace(/^[A-Za-z]:\\Users\\[^\\]+/, "~");
</script>

<div class="palette-backdrop" role="presentation" onclick={onclose} transition:fade={{ duration: 120 }}></div>
<div class="palette folders" role="dialog" aria-label="Switch folder" transition:pop={{ duration: 180, start: 0.97 }}>
  <input
    class="palette-input"
    use:focus
    bind:value={q}
    placeholder="Switch folder…"
    aria-label="Switch folder"
    onkeydown={(e) => {
      if (e.key === "Escape") onclose();
      else if (e.key === "ArrowDown") {
        e.preventDefault();
        active = Math.min(shown.length, active + 1);
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        active = Math.max(0, active - 1);
      } else if (e.key === "Enter") {
        e.preventDefault();
        if (active === shown.length) onadd();
        else if (shown[active]) onswitch(shown[active].id, e.metaKey || e.ctrlKey);
      }
    }}
  />
  <div class="palette-results" role="listbox">
    {#each shown as r, i (r.id)}<div class="folder-row" class:active={i === active} role="option" aria-selected={i === active} tabindex="-1" onmousemove={() => (active = i)}>
        <button class="folder-main" onclick={(e) => onswitch(r.id, e.metaKey || e.ctrlKey)} title={"Open " + r.path}
          ><FolderGit2 size={16} /><span class="folder-text"
            ><strong>{r.name}</strong><small>{home(r.path)}</small></span
          >{#if r.branch}<span class="folder-branch"><GitBranch size={11} />{r.branch}</span>{/if}{#if r.id === current}<Check size={14} class="folder-current" />{/if}</button
        ><button class="icon-button" title="Open in a new tab (⌘↵)" aria-label={"Open " + r.name + " in a new tab"} onclick={() => onswitch(r.id, true)}
          ><ExternalLink size={14} /></button
        ><button class="icon-button" title="Stop serving this folder (its files are not touched)" aria-label={"Remove " + r.name} onclick={() => onremove(r)}
          ><X size={14} /></button
        >
      </div>{/each}
    <button class="folder-add" class:active={active === shown.length} onmousemove={() => (active = shown.length)} onclick={onadd}><Plus size={15} />Add a folder…</button>
  </div>
  <div class="palette-footer"><span><kbd>↵</kbd> switch</span><span><kbd>⌘</kbd><kbd>↵</kbd> new tab</span><span><kbd>esc</kbd> close</span></div>
</div>
