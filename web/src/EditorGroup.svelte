<script lang="ts">
  // One editor group: its tabs, the open file (viewer, editor, preview, diff or results), its outline,
  // and its own Back/Forward history. The folder's code view lays several of these side by side or one
  // above another (split editors); tabs can be dragged within a group, between groups, or onto a group's
  // edge to split it.
  import { untrack } from "svelte";
  import { api } from "./api";
  import { outline as cachedOutline } from "./filecache";
  import CodeViewer, { type ViewState } from "./CodeViewer.svelte";
  import Editor from "./Editor.svelte";
  import FileIcon from "./FileIcon.svelte";
  import MarkdownView from "./MarkdownView.svelte";
  import MediaView from "./MediaView.svelte";
  import { kindOf } from "./file-kind";
  import { definitions, isExternal, lspStateFor, lspStatus, lspUsable } from "./definition";
  import { confirmDiscard } from "./confirm.svelte";
  import { tabDrag, startTabDrag, endTabDrag } from "./tabdrag.svelte";
  import { fade, fly, slide } from "./motion";
  import { flip } from "svelte/animate";
  import {
    FileCode2,
    Search,
    FolderOpen,
    ChevronRight,
    GitCompare,
    PanelRightClose,
    ListTree,
    X,
    Pencil,
    ArrowLeft,
    ArrowRight,
    Eye,
    Code2,
    Columns2,
    Rows2,
  } from "@lucide/svelte";

  export type GroupState = { tabs: string[]; selected: string };
  export type DropTarget = "center" | "left" | "right" | "up" | "down";
  let {
    id,
    repoName,
    active = false,
    alone = true,
    initial,
    disk = null,
    onerror,
    onlocation,
    onfocus,
    onstate,
    ondirty,
    oncheck,
    onreveal,
    onsplit,
    ondroptab,
    ondropsplit,
    onempty,
    onbrowse,
  }: {
    id: string;
    repoName: string;
    /** The group keyboard shortcuts and the explorer act on. */
    active?: boolean;
    /** The only group (it cannot close itself). */
    alone?: boolean;
    initial: GroupState;
    disk?: { path: string; hash: string; exists: boolean; n: number } | null;
    onerror: (message: string) => void;
    onlocation?: (path: string, line: number) => void;
    onfocus: () => void;
    onstate: (s: GroupState) => void;
    ondirty?: (dirty: boolean) => void;
    oncheck: (path: string) => void;
    /** Show the file in the explorer. */
    onreveal: (path: string) => void;
    /** Split this group, showing the file (and where it is) in the new one. */
    onsplit: (direction: "right" | "down", path: string, view: ViewState | null) => void;
    /** A tab was dropped on this group's tab strip, before the tab at index. */
    ondroptab: (from: string, path: string, index: number) => void;
    /** A tab was dropped on this group's body: into it (center) or onto an edge (split). */
    ondropsplit: (from: string, path: string, where: DropTarget) => void;
    /** Its last tab closed. */
    onempty: () => void;
    onbrowse?: () => void;
  } = $props();

  let selected = $state(untrack(() => initial.selected)),
    tabs = $state<string[]>(untrack(() => [...initial.tabs])),
    mode = $state<"source" | "diff" | "results">("source"),
    outline = $state<any[]>([]),
    showOutline = $state(false),
    results = $state<{ path: string; line: number; content: string; kind?: string; column?: number; end?: number }[]>([]),
    resultsTitle = $state(""),
    diff = $state<any[]>([]),
    line = $state(1),
    version = $state(0),
    loading = $state(false),
    editing = $state(false),
    dirty = $state(false);
  let cursor = { line: 0, character: 0, hash: "" };
  $effect(() => onstate({ tabs: [...tabs], selected }));
  $effect(() => ondirty?.(editing && dirty));

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

  // Each tab remembers where it was left (scroll, caret, selection); showing it again goes back there.
  const views = new Map<string, ViewState>();
  let restoreView = $state<ViewState | null>(null),
    selectAt = $state<{ col: number; len: number } | null>(null),
    viewer = $state<ReturnType<typeof CodeViewer>>();

  function open(path: string, at = 1, sel: { col: number; len: number } | null = null, fromTab = false) {
    if (path !== selected) leave(() => openNow(path, at, sel, fromTab));
    else openNow(path, at, sel, fromTab);
  }
  function openNow(path: string, at: number, sel: { col: number; len: number } | null = null, fromTab = false) {
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
    oncheck(path);
    void loadOutline();
  }

  /** Opens a file here, at a line, selecting a match there if given. */
  export function openFile(path: string, at = 1, col?: number, len?: number) {
    open(path, at, col !== undefined && len ? { col, len } : null);
  }
  /** Ctrl+F: find in the open file (the viewer's own find; the editor keeps the browser's). */
  export function find(): boolean {
    if (editing || mode !== "source" || !viewer) return false;
    return viewer.find() ?? false;
  }
  /** The open file and where it is shown (for splitting). */
  export function current(): { path: string; view: ViewState | null } {
    return { path: selected, view: viewer && mode === "source" && !editing ? viewer.viewState() : null };
  }
  /** Shows a file brought from another group (or a split), with its view. */
  export function addTab(path: string, index = tabs.length, view: ViewState | null = null) {
    if (view) views.set(path, view);
    if (!tabs.includes(path)) {
      const next = [...tabs];
      next.splice(Math.max(0, Math.min(index, next.length)), 0, path);
      tabs = next;
    }
    open(path, 1, null, true);
  }
  /** Gives up a tab (moved to another group): its view goes with it. */
  export function takeTab(path: string): ViewState | null {
    let view = views.get(path) ?? null;
    if (path === selected && viewer && mode === "source" && !editing) view = viewer.viewState();
    removeTab(path);
    return view;
  }
  /** Moves a tab within the strip, before the tab at index. */
  export function reorder(path: string, index: number) {
    const from = tabs.indexOf(path);
    if (from < 0) return;
    const next = tabs.filter((t) => t !== path);
    next.splice(index > from ? index - 1 : index, 0, path);
    tabs = next;
  }
  export function hasUnsaved() {
    return editing && dirty;
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
        editing = false;
        dirty = false;
        if (active) onlocation?.("", 0);
      }
    }
    if (!tabs.length && !alone) onempty();
  }
  /** Closes the whole group (its tabs first, asking about unsaved edits). */
  function closeGroup() {
    if (alone) return;
    if (!tabs.length) return onempty();
    closeMany(() => false);
  }
  function closeMany(keep: (t: string, i: number) => boolean) {
    const doomed = tabs.filter((t, i) => !keep(t, i));
    if (!doomed.length) return;
    const run = () => {
      for (const t of doomed) {
        views.delete(t);
      }
      const rest = tabs.filter((t) => !doomed.includes(t));
      tabs = rest;
      if (doomed.includes(selected)) {
        if (rest.length) open(rest[rest.length - 1], 1, null, true);
        else selected = "";
      }
      if (!rest.length && !alone) onempty();
    };
    if (doomed.includes(selected)) leave(run);
    else run();
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

  // Dragging tabs: within the strip to reorder, to another group's strip to move, onto a group's body
  // to move it there (centre) or split (an edge). Where it would land is shown while dragging.
  let dropIndex = $state(-1),
    dropZone = $state<DropTarget | null>(null);
  function stripIndex(e: DragEvent) {
    const items = [...(strip?.querySelectorAll<HTMLElement>(".tab") ?? [])];
    for (let i = 0; i < items.length; i++) {
      const r = items[i].getBoundingClientRect();
      if (e.clientX < r.left + r.width / 2) return i;
    }
    return items.length;
  }
  function zoneOf(e: DragEvent, el: HTMLElement): DropTarget {
    const r = el.getBoundingClientRect();
    const x = (e.clientX - r.left) / r.width,
      y = (e.clientY - r.top) / r.height;
    const edge = 0.25;
    const d = { left: x, right: 1 - x, up: y, down: 1 - y };
    const [nearest, dist] = (Object.entries(d) as [DropTarget, number][]).sort((a, b) => a[1] - b[1])[0];
    return dist < edge ? nearest : "center";
  }

  // The tab context menu.
  // The menu's place and tab outlive its closing (it fades out after menu is cleared).
  let menu = $state<{ x: number; y: number; tab: string } | null>(null),
    menuAt = $state({ x: 0, y: 0, tab: "" });
  const menuIndex = $derived(tabs.indexOf(menuAt.tab));
  function tabMenu(e: MouseEvent, tab: string) {
    e.preventDefault();
    onfocus();
    menu = { x: Math.min(e.clientX, innerWidth - 230), y: Math.min(e.clientY, innerHeight - 280), tab };
    menuAt = menu;
  }
  function act(fn: () => void) {
    menu = null;
    fn();
  }

  // Navigation history: a definition, a reference or a search result leaves the place it was made from
  // behind; Back returns there exactly (scroll, caret, selection) and Forward comes back: Alt+← and Alt+→,
  // or the mouse's back and forward buttons.
  type Place = { path: string; line: number; view: ViewState | null };
  let trail = $state<Place[]>([]),
    ahead = $state<Place[]>([]);
  const here = (): Place | null =>
    selected ? { path: selected, line: cursor.line + 1, view: viewer && mode === "source" && !editing ? viewer.viewState() : null } : null;
  /** Goes to a place (line -1: stay, only remember where this was). */
  export function jump(path: string, at: number, column?: number, end?: number) {
    const from = here();
    if (from) trail = [...trail.slice(-99), from];
    ahead = [];
    if (at < 0) return;
    open(path, at, column != null ? { col: column, len: Math.max(0, (end ?? column) - column) } : null);
  }
  function visit(p: Place) {
    if (p.path === selected && viewer && p.view && mode === "source" && !editing) return viewer.restoreView(p.view);
    if (p.view) views.set(p.path, p.view);
    open(p.path, p.line, null, !!p.view);
  }
  function back() {
    const prev = trail.at(-1);
    const from = here();
    if (!prev) return;
    trail = trail.slice(0, -1);
    if (from) ahead = [...ahead, from];
    visit(prev);
  }
  function forward() {
    const next = ahead.at(-1);
    const from = here();
    if (!next) return;
    ahead = ahead.slice(0, -1);
    if (from) trail = [...trail, from];
    visit(next);
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
        results = found.targets.map((t) => ({ path: t.path, line: t.line, column: t.column, end: t.end, content: t.text || t.signature || "" }));
        resultsTitle = `References to ${name}`;
      } else {
        const hits = await api<{ path: string; line: number; content: string }[]>("/api/search?word=true&case=true&q=" + encodeURIComponent(name));
        // git grep's columns are bytes; the name's place on the line, in characters, selects it.
        results = hits.map((h) => {
          const at = h.content.search(new RegExp(`(?<![\\w$])${name.replace(/[$]/g, "\\$")}(?![\\w$])`));
          return { path: h.path, line: h.line, content: h.content, column: at >= 0 ? at : undefined, end: at >= 0 ? at + name.length : undefined };
        });
        resultsTitle = `${name} · whole-word matches`;
      }
      mode = "results";
    } catch (e) {
      onerror((e as Error).message);
    } finally {
      loading = false;
    }
  }

  const external = $derived(isExternal(selected));
  // An outside file shows its readable path (~ext/<token>/go/errors/errors.go → go › errors › errors.go).
  const crumbs = $derived(selected ? (external ? selected.split("/").slice(2) : selected.split("/")) : []);
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
  // Only the open file's disk events reach its viewer.
  const myDisk = $derived(disk && tabs.includes(disk.path) ? disk : null);
  // When focused, the address bar shows this group's file.
  $effect(() => {
    if (active) untrack(() => onlocation?.(selected, cursor.line + 1 || line));
  });
</script>

<svelte:window
  onmouseup={(e) => {
    if (!active) return;
    // The mouse's back and forward buttons move through the navigation history (not the browser's).
    if (e.button === 3 && trail.length) {
      e.preventDefault();
      back();
    } else if (e.button === 4 && ahead.length) {
      e.preventDefault();
      forward();
    }
  }}
  onkeydown={(e) => {
    if (menu && e.key === "Escape") menu = null;
    if (!active) return;
    // Ctrl+S outside the editor would open the browser's "Save page" dialog.
    if (e.ctrlKey && e.key.toLowerCase() === "s" && !editing) e.preventDefault();
    const backKey = e.altKey && !e.shiftKey && !e.ctrlKey && e.key === "ArrowLeft";
    const forwardKey = e.altKey && !e.shiftKey && !e.ctrlKey && e.key === "ArrowRight";
    if (backKey && trail.length) {
      e.preventDefault();
      back();
    } else if (forwardKey && ahead.length) {
      e.preventDefault();
      forward();
    }
    // Edit the open file.
    if (e.key === "Enter" && e.ctrlKey && e.shiftKey && selected && !editing) {
      e.preventDefault();
      editing = true;
    }
  }}
  onpointerdown={(e) => {
    if (menu && !(e.target as Element).closest?.(".tab-menu")) menu = null;
  }}
/>
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="source-panel" class:active-group={active} onpointerdowncapture={() => !active && onfocus()} onfocusin={() => !active && onfocus()}>
  <div class="source-tabs">
    {#if onbrowse}<button class="icon-button files-toggle" aria-label="Browse files" onclick={onbrowse}><FolderOpen size={17} /></button>{/if}
    <div
      class="tab-strip"
      bind:this={strip}
      onwheel={stripWheel}
      role="tablist"
      tabindex="-1"
      aria-label="Open files"
      ondragover={(e) => {
        if (!tabDrag?.path) return;
        e.preventDefault();
        dropIndex = stripIndex(e);
      }}
      ondragleave={(e) => {
        if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) dropIndex = -1;
      }}
      ondrop={(e) => {
        e.preventDefault();
        const index = stripIndex(e);
        dropIndex = -1;
        if (tabDrag?.path) ondroptab(tabDrag.group, tabDrag.path, index);
        endTabDrag();
      }}
    >
      {#each tabs as tab, i (tab)}{@const label = tabNames.get(tab)}<div
          class="tab"
          class:active={selected === tab}
          class:drop-before={dropIndex === i}
          class:drop-after={dropIndex === tabs.length && i === tabs.length - 1}
          animate:flip={{ duration: 160 }}
          in:fly={{ y: -4, duration: 140 }}
          draggable="true"
          ondragstart={(e) => {
            startTabDrag(id, tab);
            e.dataTransfer?.setData("text/plain", tab);
            if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
          }}
          ondragend={() => {
            endTabDrag();
            dropIndex = -1;
          }}
          oncontextmenu={(e) => tabMenu(e, tab)}
          role="presentation"
        >
          <button
            role="tab"
            aria-selected={selected === tab}
            title={tab}
            onclick={() => {
              if (tab === selected) return;
              open(tab, 1, null, true);
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
      {#if selected && textual && !external}<button
          class="icon-button"
          class:on={editing}
          title={editing ? "Editing" : "Edit file (Ctrl+Shift+Enter)"}
          aria-label="Edit file"
          onclick={() => {
            if (!editing) {
              mode = "source";
              editing = true;
              oncheck(selected);
            }
          }}><Pencil size={15} /></button
        ><button class="icon-button" class:on={mode === "diff"} title="Changes to this file" aria-label="File changes" onclick={() => (mode === "diff" ? (mode = "source") : showDiff())}
          ><GitCompare size={15} /></button
        ><button class="icon-button" class:on={showOutline} title="Outline" aria-label="Outline" onclick={() => (showOutline = !showOutline)}
          ><ListTree size={15} /></button
        >{/if}
      {#if selected}<button class="icon-button" title="Split right (Ctrl+\)" aria-label="Split right" onclick={() => onsplit("right", selected, current().view)}><Columns2 size={15} /></button>{/if}
      {#if !alone}<button class="icon-button" title="Close group" aria-label="Close group" onclick={closeGroup}><X size={15} /></button>{/if}
    </div>
  </div>
  {#if selected}<div class="breadcrumbs">
      {#if trail.length || ahead.length}<button class="crumb-back" title="Back (Alt+←)" aria-label="Back" disabled={!trail.length} onclick={back}><ArrowLeft size={12} /></button
        ><button class="crumb-back" title="Forward (Alt+→)" aria-label="Forward" disabled={!ahead.length} onclick={forward}><ArrowRight size={12} /></button
        >{/if}{#if external}<span class="crumb-external" title="A file outside this folder, reached through a definition. Read-only.">Outside the folder</span
        >{:else}<span class="crumb-root">{repoName}</span
        >{/if}{#each crumbs as segment, i}<ChevronRight size={11} />{#if i < crumbs.length - 1 && !external}<button
            class="crumb"
            title="Show in the explorer"
            onclick={() => onreveal(crumbs.slice(0, i + 1).join("/") + "/")}>{segment}</button
          >{:else}<span>{segment}</span>{/if}{/each}<span class="crumb-gap"></span>{#if shownAs === "code" || asSource}<button
          title="Go to definition of the name at the caret (F12, or Ctrl+click)"
          onclick={() => viewer?.definitionAtCaret()}
          disabled={loading || editing}>Definition</button
        ><button title="Find references to the name at the caret (Shift+F12)" onclick={references} disabled={loading || editing}>References</button>{/if}
    </div>{/if}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    class="source-body"
    ondragenter={(e) => {
      if (tabDrag?.path) e.preventDefault();
    }}
    ondragover={(e) => {
      if (!tabDrag?.path) return;
      e.preventDefault();
      dropZone = zoneOf(e, e.currentTarget as HTMLElement);
    }}
    ondragleave={(e) => {
      if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) dropZone = null;
    }}
    ondrop={(e) => {
      e.preventDefault();
      // Where it lands is worked out from the drop itself (a drop can come without a dragover first).
      const where = zoneOf(e, e.currentTarget as HTMLElement);
      dropZone = null;
      if (tabDrag?.path) ondropsplit(tabDrag.group, tabDrag.path, where);
      endTabDrag();
    }}
  >
    {#if dropZone}<div class="drop-preview {dropZone}" aria-hidden="true"></div>{/if}
    {#if mode === "results"}<div class="search-results" in:fade={{ duration: 120 }}>
        <div class="panel-heading">
          <h3>{resultsTitle}</h3>
          <span>{results.length} {results.length === 1 ? "place" : "places"}</span><button class="button small-button" onclick={() => (mode = "source")}>Back to source</button>
        </div>
        {#each results as hit}<button class="search-hit" onclick={() => jump(hit.path, hit.line, hit.column, hit.end)}
            ><strong>{hit.path.replace(/^~ext\/[^/]+\//, "")}<span>:{hit.line}</span></strong>{#if hit.content}<pre>{hit.content.trim()}</pre>{/if}</button
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
          disk={myDisk}
        />{/key}
    {:else if selected && shownAs === "markdown" && !asSource}{#key selected + version}<MarkdownView path={selected} disk={myDisk} onopen={(p, l) => openFile(p, l)} />{/key}
    {:else if selected && shownAs !== "code" && !(previewable && asSource)}{#key selected}<MediaView path={selected} viewer={shownAs} version={diskVersion} />{/key}
    {:else if selected}{#key selected + version}<CodeViewer
          bind:this={viewer}
          onjump={jump}
          onreferences={references}
          onnotice={onerror}
          disk={myDisk}
          path={selected}
          startLine={line}
          startSelect={selectAt}
          restore={restoreView}
          onposition={(l, character, hash) => {
            cursor = { line: l, character, hash };
            if (active) onlocation?.(selected, l + 1);
          }}
        />{/key}
    {:else}<div class="empty-state file-empty" in:fade={{ duration: 200 }}>
        <div class="empty-glyph"><FileCode2 size={32} /></div>
        <h2>{repoName}</h2>
        <p>Open a file from the explorer, or jump straight to one.</p>
        {#if onbrowse}<button class="button primary mobile-only" onclick={onbrowse}><FolderOpen size={15} /> Browse files</button>{/if}
        <div class="keyboard-hints">
          <span><kbd>Ctrl</kbd><kbd>P</kbd> Go to file</span>
          <span><kbd>Ctrl</kbd><kbd>Shift</kbd><kbd>O</kbd> Go to symbol</span>
          <span><kbd>Ctrl</kbd><kbd>Shift</kbd><kbd>F</kbd> Search</span>
          <span><kbd>Ctrl</kbd><kbd>\</kbd> Split right</span>
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
{#if menu}<div class="tab-menu" role="menu" style:left="{menuAt.x}px" style:top="{menuAt.y}px" transition:fade={{ duration: 90 }}>
    <button role="menuitem" onclick={() => act(() => closeTab(menuAt.tab))}>Close</button>
    <button role="menuitem" disabled={tabs.length < 2} onclick={() => act(() => closeMany((x) => x === menuAt.tab))}>Close Others</button>
    <button role="menuitem" disabled={menuIndex === tabs.length - 1} onclick={() => act(() => closeMany((_, i) => i <= menuIndex))}>Close to the Right</button>
    <button role="menuitem" onclick={() => act(() => closeMany(() => false))}>Close All</button>
    <hr />
    <button role="menuitem" onclick={() => act(() => onsplit("right", menuAt.tab, menuAt.tab === selected ? current().view : (views.get(menuAt.tab) ?? null)))}><Columns2 size={13} />Split Right</button>
    <button role="menuitem" onclick={() => act(() => onsplit("down", menuAt.tab, menuAt.tab === selected ? current().view : (views.get(menuAt.tab) ?? null)))}><Rows2 size={13} />Split Down</button>
    <hr />
    <button role="menuitem" onclick={() => act(() => void navigator.clipboard?.writeText(menuAt.tab.replace(/^~ext\/[^/]+\//, "")))}>Copy Path</button>
    {#if !isExternal(menuAt.tab)}<button role="menuitem" onclick={() => act(() => onreveal(menuAt.tab))}>Reveal in Explorer</button>{/if}
  </div>{/if}
