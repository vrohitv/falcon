<script lang="ts">
  // The language-server pill in the editor's status bar: a dot for the current file's server and a
  // menu with every server's state.
  import { onMount } from "svelte";
  import { fly } from "./motion";
  import { timeAgo } from "./api";
  import { lsp, lspStatus, lspStateFor, refreshLsp } from "./lsp.svelte";
  import { Check, Copy } from "@lucide/svelte";
  let { path }: { path: string } = $props();
  let open = $state(false),
    copied = $state(""),
    checking = $state(false),
    showWhere = $state(false);
  const home = (p: string) => p.replace(/^\/(Users|home)\/[^/]+/, "~").replace(/^[A-Za-z]:\\Users\\[^\\]+/, "~");
  // After installing: look again (Falcon finds a server wherever its installer put it, no restart).
  async function checkAgain() {
    checking = true;
    try {
      await refreshLsp();
    } finally {
      setTimeout(() => (checking = false), 300);
    }
  }
  const status = $derived(lsp.status);
  const current = $derived(lspStateFor(path, status));
  const name = $derived(current.server?.server);
  const title = $derived(
    {
      ready: `${name} is ready: ⌘-click asks it first, then falls back to syntax matches` +
        (current.server?.note ? ` (${current.server.note})` : ""),
      starting: `${name} is starting and answering…`,
      indexing: `${name} is reading the project${current.server?.note ? ` (${current.server.note})` : ""}; lookups wait for it`,
      failed: `${name} failed: ${current.server?.note || "unknown error"}. ⌘-click uses syntax matches`,
      off: `Language servers are turned off (Falcon was started with -lsp=false); ⌘-click uses syntax matches`,
      missing: `${name} is not installed (open this menu for how); ⌘-click uses syntax matches`,
      none: "No language server for this file type; ⌘-click uses syntax matches",
    }[current.state],
  );
  function rowState(s: (typeof lsp.status & {})["servers"][number]) {
    if (!s.installed) return "missing";
    if (!status?.execution) return "off";
    if (lsp.busy === s.server || s.state === "starting") return "starting";
    if (s.state === "indexing") return "indexing";
    return s.state === "failed" ? "failed" : "ready";
  }
  const label: Record<string, string> = {
    ready: "Ready",
    starting: "Starting…",
    indexing: "Indexing…",
    failed: "Failed",
    off: "Off",
    missing: "Missing",
  };
  async function toggle() {
    open = !open;
    if (open) await refreshLsp();
  }
  async function copy(cmd: string) {
    await navigator.clipboard?.writeText(cmd);
    copied = cmd;
    setTimeout(() => (copied = ""), 1500);
  }
  // While a server is starting or reading the project, its progress shows as it happens.
  $effect(() => {
    const working = status?.servers.some((s) => s.state === "starting" || s.state === "indexing");
    if (!working) return;
    const t = setInterval(() => void refreshLsp(), 2000);
    return () => clearInterval(t);
  });
  onMount(() => {
    void lspStatus();
    // Installing a server or changing settings elsewhere shows up on return.
    const focus = () => document.visibilityState === "visible" && void refreshLsp();
    window.addEventListener("focus", focus);
    document.addEventListener("visibilitychange", focus);
    return () => {
      window.removeEventListener("focus", focus);
      document.removeEventListener("visibilitychange", focus);
    };
  });
</script>

<svelte:window
  onkeydown={(e) => e.key === "Escape" && (open = false)}
  onpointerdown={(e) => {
    if (open && !(e.target as Element).closest?.(".lsp-status")) open = false;
  }}
/>
<span class="lsp-status">
  <button
    class="lsp-pill"
    data-state={current.state}
    {title}
    aria-label="Language servers"
    aria-expanded={open}
    onclick={toggle}>LSP</button
  >
  {#if open && status}<div class="lsp-menu" role="menu" transition:fly={{ y: 6, duration: 140 }}>
      <div class="lsp-menu-head">Language servers <small>for this folder</small></div>
      <div class="lsp-rows">
        {#each status.servers as s (s.server)}{@const state = rowState(s)}
          <div class="lsp-row" class:current={current.server?.server === s.server}>
            <div class="lsp-line">
              <span class="lsp-dot" data-state={state}></span>
              <span class="lsp-name">{s.server}<small>{s.languages.join(" · ")}</small></span>
              <span class="lsp-meta" data-state={state}>{label[state]}</span>
            </div>
            {#if state === "missing"}<div class="lsp-install">
                {#each (s.installs ?? []).slice(0, 2) as how, i}<div class="lsp-cmd">
                    <code title={how.command}>{how.command}</code><button class="lsp-act" title="Copy the command" onclick={() => copy(how.command)}
                      >{#if copied === how.command}<Check size={11} />{:else}<Copy size={11} />{/if}</button
                    >
                  </div>
                  <small class="lsp-where">{i ? "or: " : ""}installs to {how.where}</small>{/each}
              </div>{:else}
              {#if s.path}<small class="lsp-detail" title={s.path}>{home(s.path)}</small>{/if}
              {#if s.instances?.length}<small class="lsp-detail"
                  >{s.instances.length === 1 ? "Running" : `Running for ${s.instances.length} projects`}{#each s.instances as inst}{" · "}{inst.root === "." ? "the folder" : inst.root}{inst.state !== "ready" ? ` (${inst.state})` : ""}{/each}</small
                >{:else if s.at && s.state}<small class="lsp-detail">Used {timeAgo(s.at)}</small>{/if}
              {#if s.note && (state === "failed" || state === "ready" || state === "indexing")}<small class="lsp-detail" class:bad={state === "failed"} title={s.note}>{s.note}</small>{/if}
            {/if}
          </div>{/each}
      </div>
      <div class="lsp-foot">
        <button class="lsp-check" onclick={checkAgain} disabled={checking}>{checking ? "Checking…" : "Check again"}</button>
        <button class="lsp-where-toggle" aria-expanded={showWhere} onclick={() => (showWhere = !showWhere)}>Where Falcon looks</button>
      </div>
      {#if showWhere}<div class="lsp-searched">
          {#each status.searched ?? [] as dir}<code title={dir}>{home(dir)}</code>{/each}
        </div>{/if}
      <p class="lsp-note">
        {#if !status.execution}Language servers are off (Falcon was started with <code>-lsp=false</code>).{:else}After installing, press
          Check again: Falcon looks in the project's own <code>node_modules/.bin</code> and <code>.venv</code>, your PATH, and the
          folders installers use, so no restart is needed. Servers start on demand, one per project, and stop after 20 idle minutes.{/if}
        Without one, ⌘-click uses syntax matches.
      </p>
    </div>{/if}
</span>

<style>
  .lsp-menu-head small {
    font-weight: 400;
    color: var(--subtle);
  }
  .lsp-rows {
    max-height: min(56vh, 460px);
    overflow: auto;
  }
  .lsp-row {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 6px 8px;
    border-radius: 6px;
  }
  .lsp-row.current {
    background: var(--surface-hover);
  }
  .lsp-line {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .lsp-line .lsp-name {
    flex: 1;
    min-width: 0;
  }
  .lsp-detail,
  .lsp-where {
    display: block;
    padding-left: 15px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 10.5px;
    color: var(--subtle);
  }
  .lsp-detail.bad {
    color: var(--danger);
  }
  .lsp-install {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 4px 0 2px 15px;
  }
  .lsp-install .lsp-where {
    padding-left: 2px;
    margin-bottom: 3px;
  }
  .lsp-cmd {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .lsp-cmd code {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    padding: 4px 7px;
    border-radius: 5px;
    background: var(--input);
    border: 1px solid var(--line);
    font: 11px var(--font-mono);
    color: var(--bright);
  }
  .lsp-foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 8px 8px 2px;
    border-top: 1px solid var(--line);
    margin-top: 4px;
  }
  .lsp-check {
    padding: 4px 10px;
    border-radius: 5px;
    background: var(--accent-solid);
    color: var(--on-accent);
    font-size: 11px;
  }
  .lsp-check:disabled {
    opacity: 0.7;
  }
  .lsp-where-toggle {
    font-size: 11px;
    color: var(--muted);
  }
  .lsp-where-toggle:hover {
    color: var(--text);
  }
  .lsp-searched {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    max-height: 120px;
    overflow: auto;
    padding: 6px 8px 0;
  }
  .lsp-searched code {
    padding: 1px 5px;
    border-radius: 4px;
    background: var(--input);
    font: 10px var(--font-mono);
    color: var(--muted);
  }
  .lsp-status {
    position: relative;
    display: inline-flex;
  }
  .lsp-pill {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 2px 7px;
    border-radius: 4px;
    font: 500 11px/1.4 var(--font-ui);
    color: var(--muted);
  }
  .lsp-pill:hover,
  .lsp-pill[aria-expanded="true"] {
    color: var(--text);
    background: var(--surface-hover);
  }
  .lsp-pill::before,
  .lsp-dot {
    content: "";
    width: 7px;
    height: 7px;
    border-radius: 50%;
    flex: none;
    background: var(--subtle);
  }
  .lsp-pill[data-state="ready"]::before,
  .lsp-dot[data-state="ready"] {
    background: var(--success);
  }
  .lsp-pill[data-state="missing"]::before,
  .lsp-dot[data-state="missing"] {
    background: var(--warning);
  }
  .lsp-pill[data-state="failed"]::before,
  .lsp-dot[data-state="failed"] {
    background: var(--danger);
  }
  .lsp-pill[data-state="indexing"]::before,
  .lsp-dot[data-state="indexing"],
  .lsp-pill[data-state="starting"]::before,
  .lsp-dot[data-state="starting"] {
    background: var(--accent);
    animation: lsp-pulse 1.1s ease-in-out infinite;
  }
  .lsp-pill[data-state="none"]::before {
    background: transparent;
    box-shadow: inset 0 0 0 1px var(--subtle);
  }
  @keyframes lsp-pulse {
    50% {
      opacity: 0.35;
    }
  }
  .lsp-meta[data-state="failed"] {
    color: var(--danger);
  }
  .lsp-menu {
    position: absolute;
    bottom: calc(100% + 8px);
    right: 0;
    z-index: 40;
    width: 400px;
    max-width: calc(100vw - 24px);
    padding: 6px;
    border: 1px solid var(--line-strong);
    border-radius: 8px;
    background: var(--surface-raised);
    box-shadow: 0 16px 40px var(--shadow);
  }
  .lsp-menu-head {
    padding: 6px 8px 8px;
    font-size: 12px;
    font-weight: 550;
    color: var(--bright);
  }
  .lsp-name {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    font: 500 12px var(--font-mono);
    color: var(--text);
  }
  .lsp-name small {
    font: 11px var(--font-ui);
    color: var(--subtle);
  }
  .lsp-meta {
    font-size: 11px;
    color: var(--subtle);
  }
  .lsp-act {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 3px 7px;
    border: 1px solid var(--line-strong);
    border-radius: 4px;
    font-size: 11px;
    color: var(--text);
  }
  .lsp-act:hover {
    border-color: var(--accent);
  }
  .lsp-note {
    margin: 6px 8px 4px;
    font-size: 11px;
    line-height: 1.5;
    color: var(--subtle);
  }
  .lsp-note code {
    font: 10.5px var(--font-mono);
    color: var(--text);
  }
</style>
