<script lang="ts">
  // The code view of one folder: the sidebar (Explorer or Search) and the editors, which can be split
  // as in VS Code: columns side by side, each holding groups one above another, every group with its
  // own tabs. Ctrl+\ splits right and Ctrl+Shift+\ down; tabs drag between groups or onto a group's edge;
  // Ctrl+1 … Ctrl+4 focus a group. The layout is remembered per folder.
  import { onMount, tick, untrack } from "svelte";
  import { cachedDirectory, observe, prefetch, rememberDirectory } from "./filecache";
  import { api } from "./api";
  import { on, watch } from "./socket.svelte";
  import type { FileEntry } from "./api";
  import type { ViewState } from "./CodeViewer.svelte";
  import EditorGroup, { type GroupState, type DropTarget } from "./EditorGroup.svelte";
  import FileTree from "./FileTree.svelte";
  import SearchPanel from "./SearchPanel.svelte";
  import type { TreeFile } from "./file-tree";
  import { isExternal } from "./definition";
  import { fade, slide } from "./motion";
  import { Search, FolderOpen, RefreshCw, FilePlus } from "@lucide/svelte";
  let {
    repoID,
    repoName,
    side = "explorer",
    initialPath = "",
    initialLine = 1,
    onerror,
    ondirty,
    onlocation,
    pulse = 0,
  }: {
    repoID: string;
    repoName: string;
    /** Which sidebar pane shows; "" hides the sidebar. */
    side?: "explorer" | "search" | "";
    initialPath?: string;
    initialLine?: number;
    onerror: (message: string) => void;
    ondirty?: (dirty: boolean) => void;
    /** The open file and line changed (for the address bar). */
    onlocation?: (path: string, line: number) => void;
    /** Goes up when the folder changed on disk (from any tool): open folders are listed again. */
    pulse?: number;
  } = $props();

  // The layout: columns side by side, each with groups one above another. Sizes are shares of the space.
  type Group = { id: string; size: number; state: GroupState };
  type Column = { id: string; size: number; groups: Group[] };
  const uid = () => Math.random().toString(36).slice(2, 9);
  const layoutKey = $derived("falcon.layout:" + repoID);
  function restoreLayout(): { columns: Column[]; active: string } {
    const id = untrack(() => repoID);
    const clean = (s: GroupState): GroupState => ({
      tabs: (s.tabs || []).filter((t) => typeof t === "string" && !isExternal(t)),
      selected: isExternal(s.selected || "") ? "" : s.selected || "",
    });
    try {
      const v = JSON.parse(localStorage.getItem("falcon.layout:" + id) || "null");
      if (v && Array.isArray(v.columns) && v.columns.length) {
        const columns: Column[] = v.columns.map((c: any) => ({
          id: uid(),
          size: Number(c.size) || 1,
          groups: (c.groups || []).map((g: any) => ({ id: uid(), size: Number(g.size) || 1, state: clean(g.state || g) })),
        }));
        const flat = columns.flatMap((c) => c.groups);
        if (flat.length) return { columns: columns.filter((c) => c.groups.length), active: flat[Math.min(flat.length - 1, Number(v.active) || 0)].id };
      }
      // The tabs of an older Falcon (one group).
      const old = JSON.parse(localStorage.getItem("falcon.tabs:" + id) || "null");
      if (old && Array.isArray(old.tabs)) {
        const g = { id: uid(), size: 1, state: clean(old) };
        return { columns: [{ id: uid(), size: 1, groups: [g] }], active: g.id };
      }
    } catch {}
    const g = { id: uid(), size: 1, state: { tabs: [], selected: "" } };
    return { columns: [{ id: uid(), size: 1, groups: [g] }], active: g.id };
  }
  const start = restoreLayout();
  let columns = $state<Column[]>(start.columns),
    activeId = $state(start.active);
  const refs: Record<string, ReturnType<typeof EditorGroup>> = $state({});
  const groups = $derived(columns.flatMap((c) => c.groups));
  const dirtyGroups = $state<Record<string, boolean>>({});
  $effect(() => ondirty?.(Object.values(dirtyGroups).some(Boolean)));
  $effect(() => {
    try {
      localStorage.setItem(
        layoutKey,
        JSON.stringify({
          columns: columns.map((c) => ({ size: c.size, groups: c.groups.map((g) => ({ size: g.size, state: g.state })) })),
          active: Math.max(0, groups.findIndex((g) => g.id === activeId)),
        }),
      );
    } catch {}
  });
  const active = () => refs[activeId];
  function where(id: string): [number, number] {
    for (let c = 0; c < columns.length; c++) {
      const g = columns[c].groups.findIndex((x) => x.id === id);
      if (g >= 0) return [c, g];
    }
    return [-1, -1];
  }

  /** Splits a group: a new group to its right (or below), showing a file and where it was. */
  async function split(fromId: string, direction: "right" | "down" | "left" | "up", path = "", view: ViewState | null = null) {
    const [c, g] = where(fromId);
    if (c < 0) return "";
    const fresh: Group = { id: uid(), size: 1, state: { tabs: [], selected: "" } };
    if (direction === "right" || direction === "left") {
      const col = columns[c];
      const half = col.size / 2;
      col.size = half;
      const added: Column = { id: uid(), size: half, groups: [fresh] };
      columns.splice(direction === "right" ? c + 1 : c, 0, added);
    } else {
      const col = columns[c];
      const half = col.groups[g].size / 2;
      col.groups[g].size = half;
      fresh.size = half;
      col.groups.splice(direction === "down" ? g + 1 : g, 0, fresh);
    }
    columns = [...columns];
    activeId = fresh.id;
    await tick();
    if (path) refs[fresh.id]?.addTab(path, 0, view);
    return fresh.id;
  }
  function removeGroup(id: string) {
    if (groups.length < 2) return;
    const [c, g] = where(id);
    if (c < 0) return;
    const col = columns[c];
    const gone = col.groups[g];
    col.groups.splice(g, 1);
    // Its space goes to its neighbour.
    if (col.groups.length) col.groups[Math.max(0, g - 1)].size += gone.size;
    else {
      columns.splice(c, 1);
      if (columns.length) columns[Math.max(0, c - 1)].size += col.size;
    }
    columns = [...columns];
    delete refs[id];
    delete dirtyGroups[id];
    if (activeId === id) activeId = columns[Math.max(0, Math.min(c, columns.length - 1))].groups[0].id;
  }
  function moveTab(fromId: string, path: string, toId: string, index = Infinity) {
    if (fromId === toId) return refs[toId]?.reorder(path, index);
    const view = refs[fromId]?.takeTab(path) ?? null;
    refs[toId]?.addTab(path, index, view);
    activeId = toId;
  }
  async function dropSplit(fromId: string, path: string, toId: string, place: DropTarget) {
    // Its own group's centre: it is already there.
    if (place === "center") return fromId === toId ? undefined : moveTab(fromId, path, toId);
    // A group's only tab dropped on its own edge stays where it is.
    const from = groups.find((g) => g.id === fromId);
    if (fromId === toId && from && from.state.tabs.length < 2) return;
    const direction = place === "left" ? "left" : place === "right" ? "right" : place === "up" ? "up" : "down";
    const fresh = await split(toId, direction);
    if (!fresh) return;
    const view = refs[fromId]?.takeTab(path) ?? null;
    await tick();
    refs[fresh]?.addTab(path, 0, view);
  }

  // Dragging the line between columns, or between groups in a column, resizes them.
  function resize(e: PointerEvent, axis: "x" | "y", a: { size: number }, b: { size: number }) {
    if (e.button !== 0) return;
    e.preventDefault();
    const handle = e.currentTarget as HTMLElement;
    const container = handle.parentElement!;
    const total = axis === "x" ? container.clientWidth : container.clientHeight;
    const shares = a.size + b.size;
    const startPos = axis === "x" ? e.clientX : e.clientY;
    const startA = a.size;
    handle.setPointerCapture(e.pointerId);
    document.body.classList.add("resizing");
    const sum = (axis === "x" ? columns : columns.find((c) => c.groups.includes(a as Group))!.groups).reduce((n, x) => n + x.size, 0);
    // No group gets narrower (or shorter) than 180px, or a tenth of the space if that is less.
    const least = Math.min(shares / 2, Math.max(0.1 * sum, (180 / total) * sum));
    const move = (ev: PointerEvent) => {
      const delta = (((axis === "x" ? ev.clientX : ev.clientY) - startPos) / total) * sum;
      const next = Math.min(shares - least, Math.max(least, startA + delta));
      a.size = next;
      b.size = shares - next;
      columns = [...columns];
    };
    const end = () => {
      document.body.classList.remove("resizing");
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", end);
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", end);
  }

  // Open files are watched by the server's OS file watcher (every group's tabs); changes arrive as
  // server-sent events. Where watching is unavailable, the hash is checked on focus and tab switch.
  let disk = $state<{ path: string; hash: string; exists: boolean; n: number } | null>(null),
    watching = $state(true),
    events = 0;
  const openPaths = $derived([...new Set(groups.flatMap((g) => g.state.tabs))]);
  function deliver(e: { path: string; hash: string; exists: boolean }) {
    observe(e.path, e.hash, e.exists);
    disk = { ...e, n: ++events };
  }
  // The open files' changes arrive on the page's one connection (see socket.svelte.ts).
  $effect(() => on("file", (m) => deliver({ path: m.path, hash: m.hash, exists: m.exists })));
  $effect(() => on("watch.unavailable", () => (watching = false)));
  $effect(() => {
    if (watching) watch(openPaths);
  });
  async function check(path: string) {
    if (watching || !path) return;
    try {
      deliver(await api("/api/file/stat?path=" + encodeURIComponent(path)));
    } catch {}
  }
  $effect(() => {
    const visible = () => {
      if (document.visibilityState !== "visible") return;
      for (const g of groups) void check(g.state.selected);
    };
    window.addEventListener("focus", visible);
    document.addEventListener("visibilitychange", visible);
    return () => {
      window.removeEventListener("focus", visible);
      document.removeEventListener("visibilitychange", visible);
    };
  });

  let creating = $state(false),
    newPath = $state(""),
    query = $state("");
  // A new file is written empty with an empty expected hash, so an existing file is never replaced.
  async function createFile(e: SubmitEvent) {
    e.preventDefault();
    const path = newPath.trim().replace(/^\/+/, "");
    if (!path || path.endsWith("/")) return;
    try {
      await api("/api/file", { method: "PUT", body: JSON.stringify({ path, content: "", expected_hash: "" }) });
      creating = false;
      directories = new Map();
      await load();
      await reveal(path);
      active()?.openFile(path);
    } catch (err) {
      onerror((err as Error).message.includes("changed since") ? path + " already exists" : (err as Error).message);
    }
  }

  // The explorer: folders load as they are opened; typing filters every file (one listing, fetched once).
  let directories = $state(new Map<string, TreeFile[]>()),
    searchFiles = $state<FileEntry[] | null>(null),
    treeBusy = $state(false);
  let generation = 0,
    alive = true,
    searching: Promise<void> | undefined;
  const requests = new Map<string, Promise<void>>();
  const files = $derived.by(() => {
    const entries = new Map<string, TreeFile>();
    for (const children of directories.values())
      for (const entry of children) entries.set(entry.path, { ...entry, loaded: !!entry.directory && directories.has(entry.path) });
    if (query.trim() && searchFiles) for (const file of searchFiles) if (!entries.has(file.path)) entries.set(file.path, file);
    return [...entries.values()];
  });
  type TreeEntry = { path: string; directory: boolean; ignored: boolean; protected: boolean };
  const show = (path: string, entries: TreeEntry[]) =>
    (directories = new Map(directories).set(path, entries.map((entry) => ({ ...entry, disabled: entry.protected }))));
  function loadDirectory(path: string): Promise<void> {
    if (directories.has(path)) return Promise.resolve();
    if (requests.has(path)) return requests.get(path)!;
    const current = generation;
    const cached = cachedDirectory<TreeEntry[]>(path);
    if (cached) show(path, cached);
    const promise = (async () => {
      const entries = await api<TreeEntry[]>("/api/tree?path=" + encodeURIComponent(path));
      rememberDirectory(path, entries);
      if (alive && current === generation && JSON.stringify(entries) !== JSON.stringify(cached)) show(path, entries);
    })().finally(() => {
      if (requests.get(path) === promise) requests.delete(path);
    });
    requests.set(path, promise);
    if (cached) {
      promise.catch(() => {});
      return Promise.resolve();
    }
    return promise;
  }
  function searchPaths() {
    if (searching || searchFiles) return;
    const current = generation;
    treeBusy = true;
    searching = api<FileEntry[]>("/api/files")
      .then((next) => {
        if (alive && current === generation) searchFiles = next;
      })
      .catch((e) => onerror((e as Error).message))
      .finally(() => {
        treeBusy = false;
        searching = undefined;
      });
  }
  // The tree filters a moment after typing stops (a large folder has tens of thousands of names).
  let treeFilter = $state("");
  $effect(() => {
    const q = query;
    if (!q) {
      treeFilter = "";
      return;
    }
    const timer = setTimeout(() => (treeFilter = q), 120);
    return () => clearTimeout(timer);
  });
  $effect(() => {
    if (!query.trim() || searchFiles) return;
    const timer = setTimeout(searchPaths, 120);
    return () => clearTimeout(timer);
  });
  // The folders on screen, listed again in place (new and deleted files appear; nothing collapses).
  async function revalidate() {
    const current = generation;
    searchFiles = null;
    for (const path of [...directories.keys()]) {
      try {
        const entries = await api<TreeEntry[]>("/api/tree?path=" + encodeURIComponent(path));
        if (!alive || current !== generation) return;
        rememberDirectory(path, entries);
        const before = directories.get(path);
        if (JSON.stringify(before?.map((e) => e.path)) !== JSON.stringify(entries.map((e) => e.path))) show(path, entries);
      } catch {
        // The folder itself went away.
        if (path && alive && current === generation) {
          const next = new Map(directories);
          next.delete(path);
          directories = next;
        }
      }
    }
  }
  let seenPulse = untrack(() => pulse);
  $effect(() => {
    if (pulse === seenPulse) return;
    seenPulse = pulse;
    void revalidate();
  });
  async function load() {
    generation++;
    directories = new Map();
    requests.clear();
    searchFiles = null;
    try {
      await loadDirectory("");
      const first = groups.find((g) => g.id === activeId)?.state.selected;
      if (initialPath) {
        await reveal(initialPath);
        await tick();
        active()?.openFile(initialPath, initialLine);
      } else if (first) await reveal(first);
    } catch (e) {
      onerror((e as Error).message);
    }
  }
  /** Opens the explorer's folders down to a file (or a folder, given with a trailing slash). */
  async function reveal(path: string) {
    if (isExternal(path)) return;
    const parts = path.split("/");
    for (let i = 1; i < parts.length; i++) await loadDirectory(parts.slice(0, i).join("/")).catch(() => {});
  }
  /** Ctrl+F: find in the focused group's file. */
  export function find(): boolean {
    return active()?.find() ?? false;
  }
  /** Opens a file in the focused group (from the switcher, quick open or search). */
  export function openFile(path: string, at = 1, col?: number, len?: number) {
    void reveal(path);
    active()?.openFile(path, at, col, len);
  }

  // The explorer's width: drag its edge or use the arrow keys on it; double-click resets.
  let treePane = $state<HTMLElement>(),
    treeWidth = $state<number | null>(null),
    treeDragging = $state(false);
  try {
    const saved = Number(localStorage.getItem("falcon.treeWidth"));
    if (saved > 0) treeWidth = saved;
  } catch {}
  function setTreeWidth(width: number | null) {
    if (width !== null) {
      const room = (treePane?.parentElement?.clientWidth || 1200) - 320;
      width = Math.round(Math.min(Math.max(160, room), 640, Math.max(160, width)));
    }
    treeWidth = width;
    try {
      if (width === null) localStorage.removeItem("falcon.treeWidth");
      else localStorage.setItem("falcon.treeWidth", String(width));
    } catch {}
  }
  function startTreeResize(e: PointerEvent) {
    if (!treePane || e.button !== 0) return;
    e.preventDefault();
    const left = treePane.getBoundingClientRect().left;
    const handle = e.currentTarget as HTMLElement;
    handle.setPointerCapture(e.pointerId);
    treeDragging = true;
    document.body.classList.add("resizing");
    let frame = 0;
    const move = (ev: PointerEvent) => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => setTreeWidth(ev.clientX - left));
    };
    const end = () => {
      treeDragging = false;
      document.body.classList.remove("resizing");
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", end);
      handle.removeEventListener("pointercancel", end);
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", end);
    handle.addEventListener("pointercancel", end);
  }

  let filesOpen = $state(untrack(() => !initialPath) && matchMedia("(max-width: 760px)").matches);
  const selectedHere = $derived(groups.find((g) => g.id === activeId)?.state.selected || "");
  onMount(() => {
    void load();
    return () => {
      alive = false;
    };
  });
</script>

<svelte:window
  onkeydown={(e) => {
    if (e.key === "Escape" && filesOpen) filesOpen = false;
    // Ctrl+\ splits the focused group to the right, Ctrl+Shift+\ below.
    if (e.ctrlKey && !e.altKey && (e.key === "\\" || e.code === "Backslash")) {
      e.preventDefault();
      const cur = active()?.current();
      void split(activeId, e.shiftKey ? "down" : "right", cur?.path || "", cur?.view ?? null);
      return;
    }
    // Ctrl+1 … Ctrl+4 focus a group.
    if (e.ctrlKey && !e.shiftKey && !e.altKey && /^[1-4]$/.test(e.key)) {
      const g = groups[Number(e.key) - 1];
      if (g) {
        e.preventDefault();
        activeId = g.id;
        (document.querySelector(`[data-group="${g.id}"] .code-viewport`) as HTMLElement | null)?.focus();
      }
    }
  }}
/>
<div class="workspace" class:no-side={!side}>
  {#if filesOpen}<div class="drawer-scrim" role="presentation" onclick={() => (filesOpen = false)} transition:fade={{ duration: 140 }}></div>{/if}
  {#if side}<aside class="file-sidebar" class:open={filesOpen} bind:this={treePane} style:--tree-width={treeWidth ? treeWidth + "px" : null}>
      {#if side === "search"}<SearchPanel onopen={(path, at, col, len) => openFile(path, at, col, len)} {onerror} />
      {:else}<div class="section-label">
          Explorer<span class="explorer-actions"
            ><button
              class="icon-button"
              aria-label="New file"
              title="New file"
              onclick={() => {
                creating = !creating;
                newPath = selectedHere ? selectedHere.slice(0, selectedHere.lastIndexOf("/") + 1) : "";
              }}><FilePlus size={13} /></button
            ><button class="icon-button" aria-label="Refresh files" title="Refresh" onclick={load}><RefreshCw size={13} /></button></span
          >
        </div>
        {#if creating}<form class="file-filter new-file" onsubmit={createFile} transition:slide={{ x: 0, y: -6 }}>
            <FilePlus size={13} /><!-- svelte-ignore a11y_autofocus --><input
              aria-label="New file path"
              placeholder="path/to/new_file.go"
              bind:value={newPath}
              autofocus
              onkeydown={(e) => e.key === "Escape" && (creating = false)}
            />
          </form>{/if}
        <div class="file-filter">
          <Search size={13} /><input aria-label="Filter files" bind:value={query} placeholder="Filter files…" />
        </div>
        <div class="tree-root"><FolderOpen size={14} />{repoName}{#if treeBusy}<span>Listing…</span>{/if}</div>
        <FileTree
          {files}
          selected={selectedHere}
          label="Explorer files"
          filter={treeFilter}
          storageKey={"falcon.tree.explorer:" + repoID}
          fill
          onopen={(path) => {
            filesOpen = false;
            active()?.openFile(path);
          }}
          onexpand={loadDirectory}
          onpeek={prefetch}
        />{/if}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
      <div
        class="tree-resizer"
        class:dragging={treeDragging}
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize sidebar"
        aria-valuenow={treePane?.offsetWidth}
        aria-valuemin={160}
        aria-valuemax={640}
        tabindex="0"
        title="Drag to resize · double-click to reset"
        onpointerdown={startTreeResize}
        ondblclick={() => setTreeWidth(null)}
        onkeydown={(e) => {
          if (!treePane || (e.key !== "ArrowLeft" && e.key !== "ArrowRight")) return;
          e.preventDefault();
          setTreeWidth(treePane.offsetWidth + (e.key === "ArrowRight" ? 16 : -16));
        }}
      ></div>
    </aside>{/if}
  <div class="editor-grid">
    {#each columns as col, ci (col.id)}
      {#if ci > 0}<!-- svelte-ignore a11y_no_static_element_interactions -->
        <div class="grid-sash vertical" title="Drag to resize" onpointerdown={(e) => resize(e, "x", columns[ci - 1], col)}></div>{/if}
      <div class="editor-column" style:flex-grow={col.size}>
        {#each col.groups as g, gi (g.id)}
          {#if gi > 0}<!-- svelte-ignore a11y_no_static_element_interactions -->
            <div class="grid-sash horizontal" title="Drag to resize" onpointerdown={(e) => resize(e, "y", col.groups[gi - 1], g)}></div>{/if}
          <div class="editor-cell" data-group={g.id} style:flex-grow={g.size}>
            <EditorGroup
              bind:this={refs[g.id]}
              id={g.id}
              {repoName}
              active={g.id === activeId}
              alone={groups.length === 1}
              initial={g.state}
              {disk}
              {onerror}
              onlocation={(p, l) => g.id === activeId && onlocation?.(p, l)}
              onfocus={() => (activeId = g.id)}
              onstate={(s) => (g.state = s)}
              ondirty={(d) => (dirtyGroups[g.id] = d)}
              oncheck={check}
              onreveal={(p) => void reveal(p)}
              onsplit={(dir, p, view) => void split(g.id, dir, p, view)}
              ondroptab={(from, p, index) => moveTab(from, p, g.id, index)}
              ondropsplit={(from, p, place) => void dropSplit(from, p, g.id, place)}
              onempty={() => removeGroup(g.id)}
              onbrowse={() => (filesOpen = !filesOpen)}
            />
          </div>
        {/each}
      </div>
    {/each}
  </div>
</div>
