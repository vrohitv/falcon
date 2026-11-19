<script lang="ts">
  import { onMount, tick, untrack } from "svelte";
  import {
    ChevronRight,
    ListCollapse,
    Plus,
    Minus,
    LoaderCircle,
    Undo2,
    Check,
  } from "@lucide/svelte";
  import FileIcon from "./FileIcon.svelte";
  import { buildFileTree, flattenTree, actionablePaths } from "./file-tree";
  import type { TreeFile, TreeNode, TreeRow } from "./file-tree";
  let {
    files,
    label,
    selected = "",
    filter = "",
    storageKey = "",
    fill = false,
    maxHeight = 336,
    action = "",
    disabled = false,
    onopen,
    onaction,
    ondiscard,
    onexpand,
    onpeek,
    open = false,
    fileLabel = (path: string) => "Open " + path,
  }: {
    files: TreeFile[];
    label: string;
    selected?: string;
    filter?: string;
    storageKey?: string;
    fill?: boolean;
    maxHeight?: number;
    action?: "Stage" | "Unstage" | "Resolve" | "";
    disabled?: boolean;
    onopen: (path: string) => void;
    onaction?: (paths: string[]) => void;
    /** A second action before the first: discard the changes (the caller confirms). */
    ondiscard?: (paths: string[]) => void;
    fileLabel?: (path: string) => string;
    onexpand?: (path: string) => Promise<void>;
    // Hovering a file for a moment, e.g. to warm a cache before the click.
    onpeek?: (path: string) => void;
    /** Folders start open (a list of changes); the owner's closed ones are remembered. */
    open?: boolean;
  } = $props();
  let peekTimer: ReturnType<typeof setTimeout> | undefined;
  let expanded = $state(new Set<string>()),
    focused = $state(""),
    top = $state(0),
    height = $state(500),
    mounted = $state(false);
  let viewport: HTMLDivElement;
  let frame = 0,
    typed = "",
    typedAt = 0;
  let pending = $state(new Set<string>()),
    failures = $state(new Map<string, string>());
  const rowHeight = 28,
    overscan = 8;
  const roots = $derived(buildFileTree(files, filter));
  const searching = $derived(!!filter.trim());
  let closed = $state(new Set<string>());
  const allDirs = $derived.by(() => {
    const dirs = new Set<string>();
    for (const f of files) {
      const parts = f.path.split("/");
      for (let i = 1; i < parts.length; i++) dirs.add(parts.slice(0, i).join("/"));
    }
    return dirs;
  });
  const shown = $derived(open ? new Set([...allDirs].filter((d) => !closed.has(d))) : expanded);
  const rows = $derived(flattenTree(roots, shown, searching));
  const windowSize = $derived(Math.ceil(height / rowHeight) + 2 * overscan);
  const start = $derived(
    Math.min(
      Math.max(0, Math.floor(top / rowHeight) - overscan),
      Math.max(0, rows.length - windowSize),
    ),
  );
  const visible = $derived(rows.slice(start, start + windowSize));
  const tabStop = $derived(
    visible.some((row) => row.node.id === focused)
      ? focused
      : visible[0]?.node.id,
  );
  const count = $derived(roots.reduce((n, node) => n + node.count, 0));
  const hasFolders = $derived(roots.some((n) => !n.file));
  function save() {
    if (!storageKey || !mounted) return;
    try {
      localStorage.setItem(storageKey, JSON.stringify([...(open ? closed : expanded)]));
    } catch {}
  }
  function toggle(row: TreeRow) {
    if (searching || row.node.directory?.disabled) return;
    if (open) {
      const c = new Set(closed);
      if (row.expanded) c.add(row.node.path);
      else c.delete(row.node.path);
      closed = c;
      save();
      return;
    }
    const next = new Set(expanded);
    if (row.expanded) next.delete(row.node.path);
    else next.add(row.node.path);
    expanded = next;
    save();
  }
  async function loadChildren(node: TreeNode) {
    if (!onexpand || pending.has(node.path)) return;
    pending = new Set(pending).add(node.path);
    try {
      await onexpand(node.path);
    } catch (e) {
      failures = new Map(failures).set(node.path, (e as Error).message);
    } finally {
      const next = new Set(pending);
      next.delete(node.path);
      pending = next;
    }
  }
  function collapse() {
    expanded = new Set();
    if (open) closed = new Set(allDirs);
    focused = "";
    viewport.scrollTop = top = 0;
    save();
  }
  function scroll() {
    if (frame) return;
    frame = requestAnimationFrame(() => {
      top = viewport.scrollTop;
      frame = 0;
    });
  }
  async function reveal(path: string) {
    if (!path) return;
    const parts = path.split("/"),
      next = new Set(expanded);
    for (let i = 1; i < parts.length; i++)
      next.add(parts.slice(0, i).join("/"));
    expanded = next;
    if (open) closed = new Set([...closed].filter((c) => !next.has(c)));
    save();
    await tick();
    const i = rows.findIndex((r) => r.node.file && r.node.path === path);
    if (i >= 0) ensureVisible(i);
  }
  function ensureVisible(index: number) {
    if (!viewport) return;
    const y = index * rowHeight;
    if (y < viewport.scrollTop) viewport.scrollTop = y;
    else if (y + rowHeight > viewport.scrollTop + viewport.clientHeight)
      viewport.scrollTop = y + rowHeight - viewport.clientHeight;
    top = viewport.scrollTop;
  }
  async function focusRow(index: number) {
    const row = rows[Math.max(0, Math.min(rows.length - 1, index))];
    if (!row) return;
    focused = row.node.id;
    ensureVisible(rows.indexOf(row));
    await tick();
    viewport
      .querySelector<HTMLElement>(
        '[data-tree-id="' + CSS.escape(row.node.id) + '"]',
      )
      ?.focus({ preventScroll: true });
  }
  function activate(row: TreeRow) {
    focused = row.node.id;
    if (!row.node.file) toggle(row);
    else if (!row.node.file.disabled) onopen(row.node.path);
  }
  function keyboard(event: KeyboardEvent) {
    if ((event.target as HTMLElement).closest(".file-tree-action")) return;
    const item = (event.target as HTMLElement).closest<HTMLElement>(
      "[data-tree-id]",
    );
    const index = rows.findIndex((r) => r.node.id === item?.dataset.treeId);
    if (index < 0 || event.altKey || event.ctrlKey) return;
    const row = rows[index];
    let next = index;
    if (event.key === "ArrowDown") next++;
    else if (event.key === "ArrowUp") next--;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = rows.length - 1;
    else if (event.key === "ArrowRight") {
      if (row.node.file) return;
      if (!row.expanded) toggle(row);
      else if (row.node.children.length) next++;
    } else if (event.key === "ArrowLeft") {
      if (row.expanded && !searching) toggle(row);
      else next = rows.findIndex((r) => r.node.id === row.node.parent);
    } else if (event.key === "Enter" || event.key === " ") activate(row);
    else if (event.key.length === 1) {
      typed =
        (Date.now() - typedAt < 700 ? typed : "") +
        event.key.toLocaleLowerCase();
      typedAt = Date.now();
      for (let step = 1; step <= rows.length; step++) {
        const candidate = (index + step) % rows.length;
        if (rows[candidate].node.name.toLocaleLowerCase().startsWith(typed)) {
          next = candidate;
          break;
        }
      }
    } else return;
    event.preventDefault();
    void focusRow(next < 0 ? index : next);
  }
  function runAction(node: TreeNode) {
    if (disabled || !onaction || !node.actionable || node.actionable > 1000)
      return;
    onaction(actionablePaths(node));
  }
  $effect(() => {
    const path = selected,
      ready = mounted;
    untrack(() => {
      if (ready) void reveal(path);
    });
  });
  $effect(() => {
    filter;
    untrack(() => {
      if (viewport) viewport.scrollTop = top = 0;
    });
  });
  $effect(() => {
    const visibleRows = rows;
    untrack(() => {
      if (!onexpand) return;
      for (const row of visibleRows)
        if (
          row.expanded &&
          row.node.directory &&
          !row.node.directory.loaded &&
          !row.node.directory.disabled &&
          !failures.has(row.node.path)
        )
          void loadChildren(row.node);
    });
  });
  onMount(() => {
    try {
      const saved = JSON.parse(localStorage.getItem(storageKey) || "[]");
      if (Array.isArray(saved)) {
        const set = new Set(saved.filter((s): s is string => typeof s === "string"));
        if (open) closed = set;
        else expanded = set;
      }
    } catch {}
    mounted = true;
    const observer = new ResizeObserver(() => (height = viewport.clientHeight));
    observer.observe(viewport);
    return () => {
      observer.disconnect();
      cancelAnimationFrame(frame);
    };
  });
</script>

<div class="file-tree" class:fill>
  {#if hasFolders}<div class="file-tree-controls">
      <span
        >{onexpand && !searching
          ? "Folders"
          : count +
            (searching ? " matching " : " ") +
            (count === 1 ? "file" : "files")}</span
      >
      <button
        class="icon-button"
        aria-label={"Collapse all " + label + " folders"}
        title="Collapse all folders"
        disabled={searching || !shown.size}
        onclick={collapse}><ListCollapse size={14} /></button
      >
    </div>{/if}
  <div
    class="file-tree-viewport"
    bind:this={viewport}
    role="tree"
    tabindex={rows.length ? -1 : 0}
    aria-label={label}
    onkeydown={keyboard}
    onscroll={scroll}
    style:height={fill
      ? undefined
      : Math.min(Math.max(rowHeight, rows.length * rowHeight), maxHeight) +
        "px"}
  >
    {#if !rows.length}<p class="file-tree-empty">
        {filter ? "No files match." : "No files."}
      </p>{/if}
    <div class="file-tree-spacer" style:height={rows.length * rowHeight + "px"}>
      <div
        class="file-tree-window"
        style:transform={"translateY(" + start * rowHeight + "px)"}
      >
        {#each visible as row (row.node.id)}
          {@const node = row.node}
          <div
            class="file-tree-row"
            class:selected={!!node.file && node.path === selected}
            class:protected={node.file?.disabled || node.directory?.disabled}
            class:ignored={node.file?.ignored || node.directory?.ignored}
            data-tree-id={node.id}
            role="treeitem"
            aria-label={node.path}
            aria-level={row.depth + 1}
            aria-posinset={row.position}
            aria-setsize={row.siblings}
            aria-expanded={node.file ? undefined : row.expanded}
            aria-selected={!!node.file && node.path === selected}
            aria-disabled={node.file?.disabled ||
              node.directory?.disabled ||
              undefined}
            tabindex={tabStop === node.id ? 0 : -1}
            onfocus={(e) => {
              if (e.target === e.currentTarget) focused = node.id;
            }}
            style:padding-left={8 + Math.min(row.depth, 12) * 14 + "px"}
          >
            <button
              class="file-tree-open"
              tabindex="-1"
              aria-label={node.file
                ? fileLabel(node.path)
                : (row.expanded ? "Collapse folder " : "Expand folder ") +
                  node.path}
              title={node.file?.oldPath
                ? node.file.oldPath + " → " + node.path
                : node.path +
                  (node.file?.ignored || node.directory?.ignored
                    ? " · Git ignored"
                    : "") +
                  (node.file?.disabled || node.directory?.disabled
                    ? " · Protected"
                    : "")}
              disabled={node.file?.disabled || node.directory?.disabled}
              onpointerenter={() => {
                clearTimeout(peekTimer);
                if (onpeek && node.file && !node.file.disabled)
                  peekTimer = setTimeout(() => onpeek(node.path), 120);
              }}
              onpointerleave={() => clearTimeout(peekTimer)}
              onclick={(e) => {
                activate(row);
                (e.currentTarget.parentElement as HTMLElement).focus({
                  preventScroll: true,
                });
              }}
            >
              {#if !node.file}<ChevronRight
                  size={12}
                  class={row.expanded
                    ? "tree-chevron expanded"
                    : "tree-chevron"}
                /><FileIcon path={node.path} folder open={row.expanded} />
              {:else}<span class="tree-chevron-spacer"></span><FileIcon
                  path={node.path}
                />{/if}
              <span class="file-tree-name">{node.name}</span>
              {#if pending.has(node.path)}<LoaderCircle
                  size={12}
                  class="tree-loading"
                />
              {:else if !node.file && !onexpand}<span class="file-tree-count"
                  >{node.count}</span
                >
              {:else if node.file?.status}<span
                  class={"file-tree-status status-" + node.file.status}
                  title={node.file.description}>{node.file.status}</span
                >{/if}
            </button>
            {#if failures.has(node.path)}<button
                class="file-tree-retry"
                title={failures.get(node.path)}
                onclick={() => {
                  const next = new Map(failures);
                  next.delete(node.path);
                  failures = next;
                  void loadChildren(node);
                }}>Retry</button
              >{/if}
            {#if ondiscard}<button
                class="icon-button file-tree-action"
                tabindex={focused === node.id ? 0 : -1}
                aria-label={"Discard changes to " + node.path}
                title={node.actionable > 1000 ? "Select a folder with at most 1000 files" : "Discard changes"}
                disabled={disabled || !node.actionable || node.actionable > 1000}
                onclick={() => !disabled && ondiscard(actionablePaths(node))}><Undo2 size={13} /></button
              >{/if}
            {#if action && onaction}<button
                class="icon-button file-tree-action"
                tabindex={focused === node.id ? 0 : -1}
                aria-label={action + (node.file ? " " : " folder ") + node.path}
                title={node.actionable > 1000
                  ? "Select a folder with at most 1000 files"
                  : action +
                    " " +
                    node.actionable +
                    (searching ? " matching" : "") +
                    (node.actionable === 1 ? " file" : " files")}
                disabled={disabled ||
                  !node.actionable ||
                  node.actionable > 1000}
                onclick={() => runAction(node)}
                >{#if action === "Unstage"}<Minus size={13} />{:else if action === "Resolve"}<Check size={13} />{:else}<Plus
                    size={13}
                  />{/if}</button
              >{/if}
          </div>
        {/each}
      </div>
    </div>
  </div>
</div>

<style>
  .file-tree {
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .file-tree.fill {
    flex: 1;
    min-height: 0;
  }
  .file-tree-controls {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 27px;
    flex-shrink: 0;
    padding: 0 9px 0 13px;
    color: var(--subtle);
    font-size: 10px;
  }
  .file-tree-controls .icon-button {
    padding: 3px;
    color: var(--subtle);
  }
  .file-tree-viewport {
    overflow: auto;
    min-height: 0;
    overscroll-behavior: contain;
  }
  .fill .file-tree-viewport {
    flex: 1;
  }
  .file-tree-spacer {
    position: relative;
  }
  .file-tree-window {
    position: absolute;
    inset: 0 0 auto;
  }
  .file-tree-row {
    display: flex;
    align-items: center;
    height: 28px;
    padding-right: 6px;
    outline: none;
  }
  .file-tree-row:hover {
    background: var(--surface-hover);
  }
  .file-tree-row.selected {
    background: var(--accent-muted);
    box-shadow: inset 2px 0 var(--accent);
  }
  .file-tree-row:focus-visible {
    outline: 1px solid var(--accent);
    outline-offset: -1px;
  }
  .file-tree-open {
    display: flex;
    align-items: center;
    gap: 5px;
    min-width: 0;
    flex: 1;
    height: 100%;
    padding: 0;
    text-align: left;
    color: var(--text);
    font-size: 12px;
    border-radius: 0;
  }
  .file-tree-open:disabled {
    opacity: 0.6;
  }
  .file-tree-row.ignored .file-tree-open {
    opacity: 0.48;
  }
  .file-tree-row :global(.tree-loading) {
    animation: tree-spin 1s linear infinite;
  }
  .file-tree-retry {
    font-size: 10px;
    color: var(--warning);
  }
  @keyframes tree-spin {
    to {
      transform: rotate(360deg);
    }
  }
  .file-tree-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
    flex: 1;
  }
  .file-tree-row :global(.tree-chevron) {
    flex-shrink: 0;
    color: var(--subtle);
  }
  .file-tree-row :global(.tree-chevron.expanded) {
    transform: rotate(90deg);
  }
  .tree-chevron-spacer {
    width: 12px;
    flex-shrink: 0;
  }
  .file-tree-count,
  .file-tree-status {
    font: 10px var(--font-mono);
    color: var(--subtle);
    padding: 0 5px;
  }
  .file-tree-status.status-A,
  .file-tree-status.status-\? {
    color: var(--success);
  }
  .file-tree-status.status-M,
  .file-tree-status.status-R {
    color: var(--warning);
  }
  .file-tree-status.status-D,
  .file-tree-status.status-U {
    color: var(--danger);
  }
  .file-tree-action {
    padding: 4px;
    color: var(--subtle);
    flex-shrink: 0;
  }
  .file-tree-action:hover {
    color: var(--bright);
  }
  .file-tree-empty {
    padding: 6px 13px;
    margin: 0;
    font-size: 11px;
    color: var(--subtle);
  }
</style>
