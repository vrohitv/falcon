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
    copied = $state("");
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
      off: `${name} is installed but Allow commands is off; ⌘-click uses syntax matches`,
      missing: `${name} is not installed; ⌘-click uses syntax matches`,
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
      <div class="lsp-menu-head">Language servers</div>
      {#each status.servers as s (s.server)}{@const state = rowState(s)}
        <div class="lsp-row" class:current={current.server?.server === s.server}>
          <span class="lsp-dot" data-state={state}></span>
          <span class="lsp-name"
            >{s.server}<small
              >{s.languages.join(" · ")}{#if s.at && s.state && state !== "starting"}{" · used " + timeAgo(s.at)}{/if}</small
            >{#if s.note && (state === "failed" || state === "ready" || state === "indexing")}<small
                class="lsp-note-line"
                class:bad={state === "failed"}
                title={s.note}>{s.note}</small
              >{/if}</span
          >
          {#if state === "missing" && s.install}<button
              class="lsp-act"
              title={"Copy: " + s.install}
              onclick={() => copy(s.install!)}
              >{#if copied === s.install}<Check size={11} />Copied{:else}<Copy size={11} />Install{/if}</button
            >{:else}<span class="lsp-meta" data-state={state}>{label[state]}</span>{/if}
        </div>{/each}
      <p class="lsp-note">
        {#if !status.execution}Language servers are off (Falcon was started with <code>-lsp=false</code>).{:else}Servers start
          on demand and keep running while you work; one idle for 20 minutes stops.{/if}
        Without one, ⌘-click uses syntax matches.
      </p>
    </div>{/if}
</span>

<style>
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
  .lsp-note-line {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .lsp-note-line.bad {
    color: var(--danger) !important;
  }
  .lsp-menu {
    position: absolute;
    bottom: calc(100% + 8px);
    right: 0;
    z-index: 40;
    width: 330px;
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
  .lsp-row {
    display: flex;
    align-items: center;
    gap: 9px;
    padding: 7px 8px;
    border-radius: 5px;
  }
  .lsp-row.current {
    background: var(--surface-hover);
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
