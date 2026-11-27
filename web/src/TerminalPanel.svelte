<script lang="ts">
  // The terminal panel, as in VS Code: below the editors, terminals as tabs, each tab split into panes
  // side by side; new (Ctrl+Shift+`), split, kill, maximize, hide (Ctrl+`). Sessions live on the server:
  // hiding the panel or reloading the page leaves them running, and they come back with their screens.
  import { onMount, tick, untrack } from "svelte";
  import { api } from "./api";
  import { openTerminal, socketState, send } from "./socket.svelte";
  import Terminal from "./Terminal.svelte";
  import { Plus, Columns2, Trash2, Maximize2, Minimize2, X, SquareTerminal, Search, ChevronUp, ChevronDown } from "@lucide/svelte";
  let {
    repoID,
    folder,
    height = $bindable(280),
    maximized = $bindable(false),
    onopenfile,
    onhide,
    onerror,
  }: {
    repoID: string;
    folder: string;
    height?: number;
    maximized?: boolean;
    onopenfile: (path: string, line: number, column: number) => void;
    onhide: () => void;
    onerror: (message: string) => void;
  } = $props();

  type Pane = { id: string; title: string };
  type Tab = { key: string; panes: Pane[] };
  const layoutKey = untrack(() => "falcon.terminals:" + repoID);
  let tabs = $state<Tab[]>([]),
    activeTab = $state(""),
    activePane = $state(""),
    loaded = $state(false),
    busy = $state(false),
    off = $state(false);
  const refs: Record<string, ReturnType<typeof Terminal>> = $state({});
  const uid = () => Math.random().toString(36).slice(2, 9);
  const current = $derived(tabs.find((t) => t.key === activeTab));
  $effect(() => {
    // Saved only once restored: the empty panel of a page that is loading must not overwrite it.
    const layout = { tabs: tabs.map((t) => t.panes.map((p) => p.id)), active: activePane };
    if (!loaded) return;
    try {
      localStorage.setItem(layoutKey, JSON.stringify(layout));
    } catch {}
  });
  // Numbered titles, as VS Code does: bash, bash (2), …
  function titled(title: string) {
    const used = tabs.flatMap((t) => t.panes).filter((p) => p.title.replace(/ \(\d+\)$/, "") === title).length;
    return used ? `${title} (${used + 1})` : title;
  }

  /** A new terminal: in a new tab, or split beside the focused one. */
  export async function create(split = false) {
    if (busy) return;
    busy = true;
    try {
      const pane = refs[activePane];
      void pane;
      const s = await openTerminal(100, 30);
      const p = { id: s.id, title: titled(s.title) };
      if (split && current) current.panes = [...current.panes, p];
      else {
        const t = { key: uid(), panes: [p] };
        tabs = [...tabs, t];
        activeTab = t.key;
      }
      activePane = s.id;
      await tick();
      refs[s.id]?.focus();
    } catch (e) {
      const message = (e as Error).message;
      if (message.includes("turned off")) off = true;
      onerror(message);
    } finally {
      busy = false;
    }
  }
  /** Ends a terminal (its shell and what it runs). */
  function kill(id: string) {
    send({ t: "term.close", id });
    remove(id);
  }
  function remove(id: string) {
    const next = tabs.map((t) => ({ ...t, panes: t.panes.filter((p) => p.id !== id) })).filter((t) => t.panes.length);
    tabs = next;
    delete refs[id];
    if (activePane === id || !next.some((t) => t.key === activeTab)) {
      const t = next.find((x) => x.key === activeTab) ?? next.at(-1);
      activeTab = t?.key ?? "";
      activePane = t?.panes.at(-1)?.id ?? "";
    }
    if (!next.length) onhide();
  }
  /** A new shell in place of one whose session is gone: same tab, same place among its panes. */
  async function replace(id: string) {
    try {
      const s = await openTerminal(100, 30);
      const old = tabs.flatMap((t) => t.panes).find((p) => p.id === id);
      const fresh = { id: s.id, title: old?.title ?? titled(s.title) };
      tabs = tabs.map((t) => ({ ...t, panes: t.panes.map((p) => (p.id === id ? fresh : p)) }));
      delete refs[id];
      if (activePane === id) activePane = s.id;
    } catch (e) {
      remove(id);
      onerror((e as Error).message);
    }
  }
  export function focus() {
    refs[activePane]?.focus();
  }

  // Back to the sessions still running: in the order the panel had them, then any opened elsewhere.
  onMount(async () => {
    let saved: { tabs: string[][]; active: string } = { tabs: [], active: "" };
    try {
      saved = JSON.parse(localStorage.getItem(layoutKey) || "null") ?? saved;
    } catch {}
    try {
      const r = await api<{ enabled: boolean; sessions: { id: string; title: string }[] }>("/api/terminals");
      if (!r.enabled) {
        off = true;
        loaded = true;
        return;
      }
      const alive = new Map(r.sessions.map((s) => [s.id, s]));
      const restored: Tab[] = [];
      const placed = new Set<string>();
      const counts = new Map<string, number>();
      const name = (t: string) => {
        const n = (counts.get(t) ?? 0) + 1;
        counts.set(t, n);
        return n > 1 ? `${t} (${n})` : t;
      };
      for (const ids of saved.tabs ?? []) {
        const panes = ids.filter((id) => alive.has(id)).map((id) => (placed.add(id), { id, title: name(alive.get(id)!.title) }));
        if (panes.length) restored.push({ key: uid(), panes });
      }
      for (const s of r.sessions) if (!placed.has(s.id)) restored.push({ key: uid(), panes: [{ id: s.id, title: name(s.title) }] });
      tabs = restored;
      const at = restored.find((t) => t.panes.some((p) => p.id === saved.active)) ?? restored.at(-1);
      activeTab = at?.key ?? "";
      activePane = at?.panes.find((p) => p.id === saved.active)?.id ?? at?.panes.at(-1)?.id ?? "";
    } catch (e) {
      onerror((e as Error).message);
    }
    loaded = true;
    if (!tabs.length && !off) await create();
  });

  // The panel's height: drag its top edge; double-click restores the usual height.
  function startResize(e: PointerEvent) {
    if (e.button !== 0) return;
    e.preventDefault();
    const handle = e.currentTarget as HTMLElement;
    handle.setPointerCapture(e.pointerId);
    const start = e.clientY,
      from = height;
    const room = (handle.closest(".stage-view") as HTMLElement | null)?.clientHeight ?? innerHeight;
    document.body.classList.add("resizing-rows");
    maximized = false;
    const move = (ev: PointerEvent) => (height = Math.round(Math.max(90, Math.min(room - 80, from + start - ev.clientY))));
    const up = () => {
      document.body.classList.remove("resizing-rows");
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", up);
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", up);
  }

  // Find in the focused terminal's output (Ctrl+F while it has focus).
  let finding = $state(false),
    query = $state(""),
    findInput = $state<HTMLInputElement>();
  function find(back = false) {
    if (!refs[activePane]?.find(query, back) && query) findMiss = true;
    else findMiss = false;
  }
  let findMiss = $state(false);
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<section
  class="terminal-panel"
  class:maximized
  style:height={maximized ? null : height + "px"}
  aria-label="Terminal"
  onkeydown={(e) => {
    if (e.ctrlKey && !e.shiftKey && e.key.toLowerCase() === "f") {
      e.preventDefault();
      e.stopPropagation();
      finding = true;
      requestAnimationFrame(() => findInput?.select());
    }
  }}
>
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="terminal-sash" title="Drag to resize · double-click for the usual height" onpointerdown={startResize} ondblclick={() => ((height = 280), (maximized = false))}></div>
  <header class="terminal-bar">
    <span class="terminal-label"><SquareTerminal size={13} />Terminal</span>
    <div class="terminal-tabs" role="tablist" aria-label="Terminals">
      {#each tabs as t (t.key)}<button
          role="tab"
          aria-selected={t.key === activeTab}
          class:active={t.key === activeTab}
          title={t.panes.map((p) => p.title).join(" · ")}
          onclick={async () => {
            activeTab = t.key;
            activePane = t.panes.at(-1)!.id;
            await tick();
            focus();
          }}
          onauxclick={(e) => e.button === 1 && t.panes.forEach((p) => kill(p.id))}
          >{t.panes.map((p) => p.title).join(" | ")}</button
        >{/each}
    </div>
    <span class="terminal-actions">
      <button class="icon-button" title="New terminal (Ctrl+Shift+`)" aria-label="New terminal" disabled={busy || off} onclick={() => create(false)}><Plus size={15} /></button>
      <button class="icon-button" title="Split terminal" aria-label="Split terminal" disabled={busy || off || !current} onclick={() => create(true)}><Columns2 size={14} /></button>
      <button class="icon-button" title="Kill terminal" aria-label="Kill terminal" disabled={!activePane} onclick={() => kill(activePane)}><Trash2 size={14} /></button>
      <button class="icon-button" title={maximized ? "Restore panel size" : "Maximize panel"} aria-label={maximized ? "Restore panel size" : "Maximize panel"} onclick={() => (maximized = !maximized)}
        >{#if maximized}<Minimize2 size={14} />{:else}<Maximize2 size={14} />{/if}</button
      >
      <button class="icon-button" title="Hide panel (Ctrl+`)" aria-label="Hide panel" onclick={onhide}><X size={15} /></button>
    </span>
  </header>
  <div class="terminal-body">
    {#if off}<p class="terminal-note">Terminals are turned off: Falcon was started with <code>-terminal=false</code>, or serves a network without <code>-terminal</code>.</p>
    {:else if !socketState.open}<p class="terminal-note subtle">Connecting…</p>{/if}
    {#each tabs as t (t.key)}<div class="terminal-tab" class:shown={t.key === activeTab}>
        {#each t.panes as p, i (p.id)}{#if i > 0}<div class="terminal-split-line"></div>{/if}<div
            class="terminal-pane"
            class:focused={p.id === activePane && t.panes.length > 1}
          >
            <Terminal
              bind:this={refs[p.id]}
              id={p.id}
              {folder}
              visible={t.key === activeTab}
              onfocus={() => (activePane = p.id)}
              onlink={onopenfile}
              onexit={(code) => {
                // Gone (null) is not an exit: the session ended without its shell saying so (Falcon was
                // restarted). A fresh shell takes its place rather than the panel closing under the user.
                if (code === null) return void replace(p.id);
                if (code) onerror(`${p.title} exited with code ${code}`);
                remove(p.id);
              }}
            />
          </div>{/each}
      </div>{/each}
    {#if finding}<div class="terminal-find" role="search">
        <Search size={13} /><input
          bind:this={findInput}
          bind:value={query}
          class:bad={findMiss}
          aria-label="Find in terminal"
          placeholder="Find"
          oninput={() => find()}
          onkeydown={(e) => {
            if (e.key === "Enter") find(e.shiftKey);
            if (e.key === "Escape") {
              finding = false;
              focus();
            }
          }}
        /><button class="icon-button" aria-label="Previous match" onclick={() => find(true)}><ChevronUp size={14} /></button><button class="icon-button" aria-label="Next match" onclick={() => find()}
          ><ChevronDown size={14} /></button
        ><button class="icon-button" aria-label="Close find" onclick={() => ((finding = false), focus())}><X size={14} /></button>
      </div>{/if}
  </div>
</section>

<style>
  .terminal-panel {
    position: relative;
    flex: none;
    display: flex;
    flex-direction: column;
    min-height: 90px;
    border-top: 1px solid var(--line);
    background: var(--editor);
  }
  .terminal-panel.maximized {
    flex: 1;
  }
  .terminal-sash {
    position: absolute;
    top: -3px;
    left: 0;
    right: 0;
    height: 6px;
    z-index: 5;
    cursor: row-resize;
  }
  .terminal-sash:hover,
  .terminal-sash:active {
    background: var(--accent);
    opacity: 0.6;
  }
  .terminal-bar {
    flex: none;
    display: flex;
    align-items: center;
    gap: 8px;
    height: 32px;
    padding: 0 6px 0 12px;
    border-bottom: 1px solid var(--line);
    background: var(--sidebar);
  }
  .terminal-label {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.02em;
    color: var(--muted);
  }
  .terminal-tabs {
    flex: 1;
    display: flex;
    gap: 2px;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .terminal-tabs button {
    flex: none;
    max-width: 220px;
    padding: 4px 10px;
    border-radius: 0;
    border-bottom: 2px solid transparent;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 12px;
    color: var(--muted);
  }
  .terminal-tabs button:hover {
    color: var(--text);
  }
  .terminal-tabs button.active {
    color: var(--bright);
    border-bottom-color: var(--accent);
  }
  .terminal-actions {
    display: flex;
    align-items: center;
    gap: 1px;
  }
  .terminal-body {
    position: relative;
    flex: 1;
    min-height: 0;
  }
  .terminal-tab {
    position: absolute;
    inset: 0;
    display: flex;
    visibility: hidden;
  }
  .terminal-tab.shown {
    visibility: visible;
  }
  .terminal-pane {
    position: relative;
    flex: 1;
    min-width: 0;
  }
  .terminal-pane.focused::after {
    content: "";
    position: absolute;
    inset: 0;
    pointer-events: none;
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--accent) 45%, transparent);
  }
  .terminal-split-line {
    flex: none;
    width: 1px;
    background: var(--line);
  }
  .terminal-note {
    margin: 14px 16px;
    font-size: 12px;
    color: var(--muted);
  }
  .terminal-note.subtle {
    position: absolute;
    right: 12px;
    top: 0;
    z-index: 2;
  }
  .terminal-find {
    position: absolute;
    top: 6px;
    right: 22px;
    z-index: 6;
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 3px 4px 3px 8px;
    border: 1px solid var(--line-strong);
    border-radius: 6px;
    background: var(--surface-raised);
    box-shadow: 0 8px 24px var(--shadow);
    color: var(--subtle);
  }
  .terminal-find input {
    width: 200px;
    height: 24px;
    padding: 0 6px;
    border: 1px solid var(--line-strong);
    border-radius: 4px;
    background: var(--input);
    font: 12px var(--font-mono);
    color: var(--bright);
  }
  .terminal-find input.bad {
    border-color: var(--danger);
  }
  :global(body.resizing-rows) {
    cursor: row-resize;
    user-select: none;
  }
</style>
