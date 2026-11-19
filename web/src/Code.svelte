<script lang="ts">
  // The code view of one folder: the sidebar (Explorer or Search), open tabs, and the viewer or editor.
  import { onMount, untrack } from "svelte";
  import { cachedDirectory, observe, outline as cachedOutline, prefetch, rememberDirectory } from "./filecache";
  import { api, apiURL } from "./api";
  import type { FileEntry } from "./api";
  import CodeViewer, { type ViewState } from "./CodeViewer.svelte";
  import Editor from "./Editor.svelte";
  import FileTree from "./FileTree.svelte";
  import FileIcon from "./FileIcon.svelte";
  import SearchPanel from "./SearchPanel.svelte";
  import MarkdownView from "./MarkdownView.svelte";
  import MediaView from "./MediaView.svelte";
  import { kindOf } from "./file-kind";
  import type { TreeFile } from "./file-tree";
  import { definitions, lspStateFor, lspStatus, lspUsable } from "./definition";
  import { confirmDiscard } from "./confirm.svelte";
  import { fade, fly, slide } from "./motion";
  import { flip } from "svelte/animate";
  import {
    FileCode2,
    Search,
    FolderOpen,
    ChevronRight,
    GitCompare,
    PanelRightClose,
    RefreshCw,
    ListTree,
    X,
    Pencil,
    FilePlus,
    ArrowLeft,
    Eye,
    Code2,
  } from "@lucide/svelte";
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

  const tabsKey = $derived("falcon.tabs:" + repoID);
  function restore(): { tabs: string[]; selected: string } {
    try {
      const v = JSON.parse(localStorage.getItem("falcon.tabs:" + untrack(() => repoID)) || "null");
      if (v && Array.isArray(v.tabs)) return v;
    } catch {}
    return { tabs: [], selected: "" };
  }
  const last = restore();
  let selected = $state(last.selected),
    tabs = $state<string[]>(last.tabs),
    mode = $state<"source" | "diff" | "results">("source"),
    outline = $state<any[]>([]),
    showOutline = $state(false),
    results = $state<{ path: string; line: number; content: string; kind?: string }[]>([]),
    resultsTitle = $state(""),
    diff = $state<any[]>([]),
    line = $state(1),
    version = $state(0),
    loading = $state(false),
    query = $state("");
  let cursor = { line: 0, character: 0, hash: "" };

  // Open tabs are watched by the server's OS file watcher; changes arrive as server-sent events. Where
  // watching is unavailable, the hash is checked on focus and tab switch.
  let disk = $state<{ path: string; hash: string; exists: boolean; n: number } | null>(null),
    watching = $state(true),
    events = 0;
  function deliver(e: { path: string; hash: string; exists: boolean }) {
    observe(e.path, e.hash, e.exists);
    disk = { ...e, n: ++events };
  }
  $effect(() => {
    const open = [...tabs];
    if (!open.length || !watching) return;
    let opened = false;
    const source = new EventSource(apiURL("/api/files/watch?" + open.map((p) => "path=" + encodeURIComponent(p)).join("&")));
    source.addEventListener("file", (event) => {
      opened = true;
      deliver(JSON.parse((event as MessageEvent).data));
    });
    source.onerror = () => {
      if (!opened) {
        source.close();
        watching = false;
      }
    };
    return () => source.close();
  });
  async function check(path = selected) {
    if (watching || !path) return;
    try {
      deliver(await api("/api/file/stat?path=" + encodeURIComponent(path)));
    } catch {}
  }
  $effect(() => {
    const visible = () => document.visibilityState === "visible" && void check();
    window.addEventListener("focus", visible);
    document.addEventListener("visibilitychange", visible);
    return () => {
      window.removeEventListener("focus", visible);
      document.removeEventListener("visibilitychange", visible);
    };
  });

  let editing = $state(false),
    dirty = $state(false),
    creating = $state(false),
    newPath = $state("");
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
      const parts = path.split("/");
      for (let i = 1; i < parts.length; i++) await loadDirectory(parts.slice(0, i).join("/"));
      open(path);
      editing = true;
    } catch (err) {
      onerror((err as Error).message.includes("changed since") ? path + " already exists" : (err as Error).message);
    }
  }
  // Leaving a file with unsaved edits asks first.
  function leave(then: () => void) {
    const go = () => {
      editing = false;
      dirty = false;
      then();
    };
    if (!(editing && dirty)) return go();
    void confirmDiscard(selected).then((ok) => ok && go());
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
      const revealPath = initialPath || selected;
      if (revealPath) await reveal(revealPath, revealPath === initialPath ? initialLine : 1);
    } catch (e) {
      onerror((e as Error).message);
    }
  }
  async function reveal(path: string, at = 1, sel: { col: number; len: number } | null = null) {
    const parts = path.split("/");
    for (let i = 1; i < parts.length; i++) await loadDirectory(parts.slice(0, i).join("/")).catch(() => {});
    open(path, at, sel);
  }
  /** ⌘F: find in the open file (the viewer's own find; the editor keeps the browser's). */
  export function find(): boolean {
    if (editing || mode !== "source" || !viewer) return false;
    return viewer.find() ?? false;
  }
  /** Open a file (from the switcher, quick open or search), at a line, selecting a match there if given. */
  export function openFile(path: string, at = 1, col?: number, len?: number) {
    void reveal(path, at, col !== undefined && len ? { col, len } : null);
  }
  // Each tab remembers where it was left (scroll, caret, selection); showing it again goes back there.
  const views = new Map<string, ViewState>();
  let restoreView = $state<ViewState | null>(null),
    selectAt = $state<{ col: number; len: number } | null>(null);

  let filesOpen = $state(untrack(() => !initialPath && !selected) && matchMedia("(max-width: 760px)").matches);
  function open(path: string, at = 1, sel: { col: number; len: number } | null = null, fromTab = false) {
    if (path !== selected) leave(() => openNow(path, at, sel, fromTab));
    else openNow(path, at, sel, fromTab);
  }
  function openNow(path: string, at: number, sel: { col: number; len: number } | null = null, fromTab = false) {
    filesOpen = false;
    const showing = viewer && mode === "source" && !editing;
    if (path === selected && showing) {
      // The file already on screen: its tab does nothing; a line in it is gone to without reloading.
      if (!fromTab) {
        viewer!.revealAt(at, sel);
        onlocation?.(path, at);
      }
      return;
    }
    if (selected && showing) views.set(selected, viewer!.viewState());
    restoreView = fromTab ? (views.get(path) ?? null) : null;
    selectAt = sel;
    selected = path;
    line = at;
    mode = "source";
    version++;
    if (!tabs.includes(path)) tabs = [...tabs, path].slice(-40);
    onlocation?.(path, at);
    void check(path);
    void loadOutline();
  }
  async function loadOutline() {
    if (!selected) return;
    try {
      outline = await cachedOutline(selected);
    } catch {
      outline = [];
    }
  }
  async function showDiff() {
    if (!selected) return;
    try {
      const d = await api("/api/git/diff?area=working&path=" + encodeURIComponent(selected));
      diff = d.hunks || [];
      mode = "diff";
    } catch (e) {
      onerror((e as Error).message);
    }
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
  function closeTab(tab: string) {
    if (selected === tab) leave(() => removeTab(tab));
    else removeTab(tab);
  }
  function removeTab(tab: string) {
    const i = tabs.indexOf(tab);
    tabs = tabs.filter((x) => x !== tab);
    views.delete(tab);
    if (selected === tab) {
      if (tabs.length) open(tabs[Math.min(i, tabs.length - 1)], 1, null, true);
      else {
        selected = "";
        onlocation?.("", 0);
      }
    }
  }

  // Tabs: two files with the same name show their folder too; the active tab is always scrolled into view;
  // the wheel scrolls the strip sideways.
  let strip = $state<HTMLDivElement>();
  const tabNames = $derived.by(() => {
    const count = new Map<string, number>();
    for (const t of tabs) count.set(t.split("/").at(-1)!, (count.get(t.split("/").at(-1)!) || 0) + 1);
    return new Map(
      tabs.map((t) => {
        const parts = t.split("/");
        const name = parts.at(-1)!;
        return [t, { name, dir: count.get(name)! > 1 && parts.length > 1 ? parts.slice(-2, -1)[0] : "" }];
      }),
    );
  });
  $effect(() => {
    void selected;
    void tabs.length;
    requestAnimationFrame(() => strip?.querySelector(".tab.active")?.scrollIntoView({ block: "nearest", inline: "nearest" }));
  });
  function stripWheel(e: WheelEvent) {
    if (!strip || Math.abs(e.deltaX) > Math.abs(e.deltaY)) return;
    strip.scrollLeft += e.deltaY;
    e.preventDefault();
  }

  // Definition jumps leave a trail; Back (Ctrl+-, Alt+←) retraces it.
  let viewer = $state<ReturnType<typeof CodeViewer>>(),
    trail = $state<{ path: string; line: number }[]>([]);
  function jump(path: string, at: number) {
    if (selected) trail = [...trail.slice(-49), { path: selected, line: cursor.line + 1 }];
    open(path, at);
  }
  function back() {
    const prev = trail.at(-1);
    if (!prev) return;
    trail = trail.slice(0, -1);
    open(prev.path, prev.line);
  }
  // References: the language server when there is one; otherwise every whole-word match.
  async function references() {
    const name = viewer?.wordAtCaret();
    if (!selected || !name) return onerror("Place the caret on a name first");
    loading = true;
    try {
      const semantic = lspUsable(lspStateFor(selected, await lspStatus()).state);
      const found = semantic
        ? await definitions({ path: selected, name, line: cursor.line, character: cursor.character, hash: cursor.hash, semantic, method: "textDocument/references" })
        : { targets: [] };
      if (found.targets.length) {
        results = found.targets.map((t) => ({ path: t.path, line: t.line, content: t.signature || "" }));
        resultsTitle = `References to ${name}`;
      } else {
        results = await api("/api/search?word=true&case=true&q=" + encodeURIComponent(name));
        resultsTitle = `${name} · whole-word matches`;
      }
      mode = "results";
    } catch (e) {
      onerror((e as Error).message);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    try {
      localStorage.setItem(tabsKey, JSON.stringify({ tabs: [...tabs], selected }));
    } catch {}
  });
  $effect(() => ondirty?.(editing && dirty));
  onMount(() => {
    void load();
    return () => {
      alive = false;
    };
  });
  const crumbs = $derived(selected ? selected.split("/") : []);
  // How the open file is shown: its viewer (Markdown, image, PDF, …), or as text when asked.
  const shownAs = $derived(selected ? kindOf(selected).viewer : "code");
  const previewable = $derived(shownAs === "markdown" || shownAs === "svg" || shownAs === "table");
  const textual = $derived(shownAs === "code" || previewable);
  let asSource = $state(false);
  $effect(() => {
    void selected;
    asSource = false;
  });
  const diskVersion = $derived(disk && disk.path === selected ? disk.n : 0);
</script>

<svelte:window
  onkeydown={(e) => {
    if (e.key === "Escape" && filesOpen) filesOpen = false;
    // ⌘S outside the editor would open the browser's "Save page" dialog.
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s" && !editing) e.preventDefault();
    if ((e.ctrlKey && !e.metaKey && e.key === "-") || (e.altKey && e.key === "ArrowLeft")) {
      if (trail.length) {
        e.preventDefault();
        back();
      }
    }
    // Edit the open file: E (when not typing), as a quick toggle.
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && e.shiftKey && selected && !editing) {
      e.preventDefault();
      editing = true;
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
                newPath = selected ? selected.slice(0, selected.lastIndexOf("/") + 1) : "";
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
          {selected}
          label="Explorer files"
          filter={query}
          storageKey={"falcon.tree.explorer:" + repoID}
          fill
          onopen={(path) => open(path)}
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
  <div class="source-panel">
    <div class="source-tabs">
      <button class="icon-button files-toggle" aria-label="Browse files" aria-expanded={filesOpen} onclick={() => (filesOpen = !filesOpen)}
        ><FolderOpen size={17} /></button
      >
      <div class="tab-strip" bind:this={strip} onwheel={stripWheel} role="tablist" aria-label="Open files">
        {#each tabs as tab (tab)}{@const label = tabNames.get(tab)}<div class="tab" class:active={selected === tab} animate:flip={{ duration: 160 }} in:fly={{ y: -4, duration: 140 }}>
            <button
              role="tab"
              aria-selected={selected === tab}
              title={tab}
              onclick={() => {
                if (tab === selected) return;
                open(tab, 1, null, true);
                void check(tab);
              }}
              onauxclick={(e) => e.button === 1 && closeTab(tab)}
              ><FileIcon path={tab} size={14} /><span class="tab-name">{label?.name}</span>{#if label?.dir}<span class="tab-dir">{label.dir}</span>{/if}</button
            >{#if editing && dirty && selected === tab}<i class="tab-dirty" title="Unsaved changes"></i>{/if}<button class="tab-close" aria-label={`Close ${tab}`} title="Close (middle-click)" onclick={() => closeTab(tab)}><X size={12} /></button>
          </div>{/each}
      </div>
      <div class="source-actions">
        {#if selected && previewable}<span class="view-toggle" role="group" aria-label="Show as"
            ><button class:on={!asSource && !editing} aria-pressed={!asSource} title="Preview" onclick={() => ((asSource = false), (mode = "source"))}><Eye size={14} />Preview</button
            ><button class:on={asSource || editing} aria-pressed={asSource} title="Source" onclick={() => ((asSource = true), (mode = "source"))}><Code2 size={14} />Source</button
            ></span
          >{/if}
        {#if selected && textual}<button
            class="icon-button"
            class:on={editing}
            title={editing ? "Editing" : "Edit file (⌘⇧Enter)"}
            aria-label="Edit file"
            onclick={() => {
              if (!editing) {
                mode = "source";
                editing = true;
                void check();
              }
            }}><Pencil size={15} /></button
          ><button class="icon-button" class:on={mode === "diff"} title="Changes to this file" aria-label="File changes" onclick={() => (mode === "diff" ? (mode = "source") : showDiff())}
            ><GitCompare size={15} /></button
          ><button class="icon-button" class:on={showOutline} title="Outline" aria-label="Outline" onclick={() => (showOutline = !showOutline)}
            ><ListTree size={15} /></button
          >{/if}
      </div>
    </div>
    {#if selected}<div class="breadcrumbs">
        {#if trail.length}<button class="crumb-back" title="Back (Ctrl+- or Alt+←)" onclick={back}><ArrowLeft size={12} /></button>{/if}<span class="crumb-root">{repoName}</span
        >{#each crumbs as segment, i}<ChevronRight size={11} />{#if i < crumbs.length - 1}<button
              class="crumb"
              title="Show in the explorer"
              onclick={() => void loadDirectory(crumbs.slice(0, i + 1).join("/"))}>{segment}</button
            >{:else}<span>{segment}</span>{/if}{/each}<span class="crumb-gap"></span>{#if shownAs === "code" || asSource}<button
            title="Go to definition of the name at the caret (F12, or ⌘-click)"
            onclick={() => viewer?.definitionAtCaret()}
            disabled={loading || editing}>Definition</button
          ><button title="Find references to the name at the caret (⇧F12)" onclick={references} disabled={loading || editing}>References</button>{/if}
      </div>{/if}
    <div class="source-body">
      {#if mode === "results"}<div class="search-results" in:fade={{ duration: 120 }}>
          <div class="panel-heading">
            <h3>{resultsTitle}</h3>
            <span>{results.length} {results.length === 1 ? "place" : "places"}</span><button class="button small-button" onclick={() => (mode = "source")}>Back to source</button>
          </div>
          {#each results as hit}<button class="search-hit" onclick={() => open(hit.path, hit.line)}
              ><strong>{hit.path}<span>:{hit.line}</span></strong>{#if hit.content}<pre>{hit.content}</pre>{/if}</button
            >{/each}{#if !results.length}<div class="empty-state">
              <Search size={28} />
              <h3>Nothing found</h3>
            </div>{/if}
        </div>
      {:else if mode === "diff"}<div class="diff-view" in:fade={{ duration: 120 }}>
          <div class="panel-heading">
            <h3>Changes to {selected.split("/").at(-1)}</h3>
            <button class="button small-button" onclick={() => (mode = "source")}>Back to source</button>
          </div>
          {#if !diff.length}<div class="empty-state">
              <GitCompare size={28} />
              <h3>No unstaged changes</h3>
              <p>This file matches what is staged (or committed).</p>
            </div>{/if}{#each diff as hunk}<div class="hunk-label">@@ -{hunk.oldStart} +{hunk.newStart} @@ {hunk.section}</div>
            {#each hunk.rows as row}<div class="diff-row {row.type}">
                <span>{row.oldLine || ""}</span><span>{row.newLine || ""}</span><code
                  >{row.type === "add" ? "+" : row.type === "del" ? "-" : " "}
                  {#if row.html}{@html row.html}{:else}{row.text}{/if}</code
                >
              </div>{/each}{/each}
        </div>
      {:else if selected && editing}{#key selected}<Editor
            path={selected}
            startLine={line}
            ondirty={(d) => (dirty = d)}
            {onerror}
            ondone={(saved) => {
              editing = false;
              dirty = false;
              if (saved) {
                version++;
                void loadOutline();
              }
            }}
            {disk}
          />{/key}
      {:else if selected && shownAs === "markdown" && !asSource}{#key selected + version}<MarkdownView path={selected} {disk} onopen={(p, l) => openFile(p, l)} />{/key}
      {:else if selected && shownAs !== "code" && !(previewable && asSource)}{#key selected}<MediaView path={selected} viewer={shownAs} version={diskVersion} />{/key}
      {:else if selected}{#key selected + version}<CodeViewer
            bind:this={viewer}
            onjump={jump}
            onreferences={references}
            onnotice={onerror}
            {disk}
            path={selected}
            startLine={line}
            startSelect={selectAt}
            restore={restoreView}
            onposition={(l, character, hash) => {
              cursor = { line: l, character, hash };
              onlocation?.(selected, l + 1);
            }}
          />{/key}
      {:else}<div class="empty-state file-empty" in:fade={{ duration: 200 }}>
          <div class="empty-glyph"><FileCode2 size={32} /></div>
          <h2>{repoName}</h2>
          <p>Open a file from the explorer, or jump straight to one.</p>
          <button class="button primary mobile-only" onclick={() => (filesOpen = true)}><FolderOpen size={15} /> Browse files</button>
          <div class="keyboard-hints">
            <span><kbd>⌘</kbd><kbd>P</kbd> Go to file</span>
            <span><kbd>⌘</kbd><kbd>T</kbd> Go to symbol</span>
            <span><kbd>⌘</kbd><kbd>⇧</kbd><kbd>F</kbd> Search</span>
            <span><kbd>⌘</kbd><kbd>K</kbd> Switch folder</span>
          </div>
        </div>{/if}
      {#if showOutline && selected}<aside class="outline" transition:slide={{ x: 24 }}>
          <div class="section-label">
            Outline<button class="icon-button" aria-label="Close outline" onclick={() => (showOutline = false)}><PanelRightClose size={13} /></button>
          </div>
          {#each outline as sym}<button onclick={() => open(selected, sym.line)}
              ><span class="symbol-kind">{sym.kind.slice(0, 2)}</span>{sym.name}<span>{sym.line}</span></button
            >{/each}{#if !outline.length}<p class="small muted">No declarations found.</p>{/if}
        </aside>{/if}
    </div>
  </div>
</div>
