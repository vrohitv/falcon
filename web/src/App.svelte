<script lang="ts">
  // Falcon's shell. One server serves many folders; this tab is on one of them, named in the URL
  // (/r/<id>/#path:line), so another tab can be on another folder, and a link opens the same file.
  import { onMount, tick, untrack } from "svelte";
  import { api, apiURL, setRepo, APIError, type Repo } from "./api";
  import { connect, disconnect, on } from "./socket.svelte";
  import { resetCaches } from "./filecache";
  import { resetLsp } from "./lsp.svelte";
  import { forgetDefinitions } from "./definition";
  import Code from "./Code.svelte";
  import GitClient from "./GitClient.svelte";
  import QuickOpen from "./QuickOpen.svelte";
  import Folders from "./Folders.svelte";
  import AddFolder from "./AddFolder.svelte";
  import ConfirmDialog from "./ConfirmDialog.svelte";
  import Mark from "./Mark.svelte";
  import { activity } from "./activity.svelte";
  import type { GitState } from "./git";
  import { confirmDialog } from "./confirm.svelte";
  import { fade, fly } from "./motion";
  import { Files, Search, FileSearch, GitBranch, FolderGit2, ChevronsUpDown, Plus, Sun, Moon, SunMoon, SquareTerminal, CircleX, TriangleAlert, ListTree } from "@lucide/svelte";
  import { problems, followProblems } from "./problems.svelte";

  let repos = $state<Repo[]>([]),
    repo = $state<Repo | null>(null),
    loaded = $state(false),
    authenticated = $state(true),
    token = $state(""),
    version = $state(""),
    view = $state<"code" | "git">("code"),
    side = $state<"explorer" | "search" | "problems" | "">("explorer"),
    quickOpen = $state(false),
    quickStart = $state(""),
    foldersOpen = $state(false),
    adding = $state(false),
    toast = $state(""),
    dirty = $state(false),
    gitPulse = $state(0),
    codePulse = $state(0),
    codeStale = false,
    ownSeq = 0,
    summary = $state<{ branch: string; head: string; changes: number } | null>(null),
    location = $state({ path: "", line: 0 });
  let code = $state<ReturnType<typeof Code>>();
  // The terminal panel: loaded the first time it opens (xterm.js stays out of the editor's way), open or
  // hidden per folder, its height remembered.
  let Panel = $state<any>(null),
    panel = $state<any>(),
    terminalOpen = $state(false),
    terminalMax = $state(false),
    terminalHeight = $state(280);
  try {
    terminalHeight = Number(localStorage.getItem("falcon.terminal.height")) || 280;
  } catch {}
  $effect(() => {
    try {
      localStorage.setItem("falcon.terminal.height", String(terminalHeight));
    } catch {}
  });
  $effect(() => {
    if (!repo) return;
    const key = "falcon.terminal.open:" + repo.id;
    untrack(() => {
      try {
        if (localStorage.getItem(key) === "1") void showTerminal(false);
      } catch {}
    });
  });
  $effect(() => {
    if (!repo) return;
    try {
      localStorage.setItem("falcon.terminal.open:" + repo.id, terminalOpen ? "1" : "0");
    } catch {}
  });
  async function showTerminal(focus = true) {
    Panel ??= (await import("./TerminalPanel.svelte")).default;
    terminalOpen = true;
    if (focus) {
      await tick();
      requestAnimationFrame(() => panel?.focus());
    }
  }
  async function newTerminal() {
    const wasOpen = terminalOpen && panel;
    await showTerminal(false);
    await tick();
    // A panel just opened makes its first terminal itself.
    if (wasOpen) await panel?.create(false);
  }
  // The mark works (wingbeats) while a request has taken more than a moment; quick ones do not flicker it.
  let working = $state(false),
    arrived = $state(false);
  // The mark dives in once when Falcon opens, then rests.
  setTimeout(() => (arrived = true), 1800);
  $effect(() => {
    const busy = activity.inflight > 0;
    const t = setTimeout(() => (working = busy), busy ? 250 : 120);
    return () => clearTimeout(t);
  });
  // Theme: follow the system, or the owner's choice (remembered on this machine).
  let theme = $state<"system" | "light" | "dark">(
    (() => {
      try {
        const t = localStorage.getItem("falcon.theme");
        return t === "light" || t === "dark" ? t : "system";
      } catch {
        return "system";
      }
    })(),
  );
  function cycleTheme() {
    theme = theme === "system" ? "light" : theme === "light" ? "dark" : "system";
    try {
      if (theme === "system") localStorage.removeItem("falcon.theme");
      else localStorage.setItem("falcon.theme", theme);
    } catch {}
    const root = document.documentElement;
    root.classList.add("theme-changing");
    if (theme === "system") delete root.dataset.theme;
    else root.dataset.theme = theme;
    setTimeout(() => root.classList.remove("theme-changing"), 260);
  }
  let toastTimer: ReturnType<typeof setTimeout>;
  function notify(message: string) {
    toast = message;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => (toast = ""), 4200);
  }

  /** Where the URL says this tab is: /r/<id>/ and #path:line. */
  function parse() {
    const m = window.location.pathname.match(/^\/r\/([^/]+)/);
    const hash = decodeURIComponent(window.location.hash.slice(1));
    const at = hash.match(/^(.*?)(?::(\d+))?$/);
    return { id: m ? decodeURIComponent(m[1]) : "", path: at?.[1] || "", line: Number(at?.[2] || 1) };
  }
  let initial = $state({ path: "", line: 1 });

  async function loadRepos() {
    try {
      const r = await api<{ repos: Repo[]; version: string }>("/api/repos");
      repos = r.repos;
      version = r.version;
      authenticated = true;
    } catch (e) {
      if (e instanceof APIError && e.status === 401) authenticated = false;
      else notify((e as Error).message);
    }
  }
  /** Put this tab on a folder (or on the start page with ""). */
  async function enter(id: string, push = true, path = "", line = 1) {
    if (dirty && repo && id !== repo.id && !(await confirmDialog("Your unsaved edits will be lost.", { title: "Leave this folder?", confirm: "Discard changes", danger: true })))
      return;
    const next = repos.find((r) => r.id === id) || null;
    if (id && !next) notify("That folder is not open in Falcon. Add it, or pick another.");
    resetCaches();
    resetLsp();
    forgetDefinitions();
    setRepo(next?.id || "");
    // The connection belongs to the folder: leaving it closes it (connect opens the next one).
    if (!next) disconnect();
    repo = next;
    summary = null;
    location = { path: "", line: 0 };
    view = "code";
    initial = { path, line };
    dirty = false;
    foldersOpen = false;
    if (next) {
      try {
        localStorage.setItem("falcon.last", next.id);
      } catch {}
      document.title = `${next.name} · Falcon`;
      void refreshSummary();
    } else document.title = "Falcon";
    const url = next ? `/r/${encodeURIComponent(next.id)}/` + (path ? "#" + encodeURI(path) + (line > 1 ? ":" + line : "") : "") : "/";
    if (push && url !== window.location.pathname + window.location.hash) history.pushState(null, "", url);
  }
  function switchTo(id: string, newTab: boolean) {
    if (newTab) {
      window.open(`/r/${encodeURIComponent(id)}/`, "_blank");
      foldersOpen = false;
    } else void enter(id);
  }
  async function removeFolder(r: Repo) {
    if (!(await confirmDialog(`Stop serving ${r.name}? Its files stay where they are; you can add it again any time.`, { title: "Remove folder", confirm: "Remove" }))) return;
    try {
      await api("/api/repos/" + encodeURIComponent(r.id), { method: "DELETE" });
      await loadRepos();
      if (repo?.id === r.id) void enter(repos[0]?.id || "");
    } catch (e) {
      notify((e as Error).message);
    }
  }
  async function added(r: Repo) {
    adding = false;
    await loadRepos();
    notify(`Added ${r.name}`);
    void enter(r.id);
  }
  // Source Control answered an action: the status bar and badge take its state, with no request.
  function gitAnswered(state?: GitState, seq?: number) {
    if (seq) ownSeq = Math.max(ownSeq, seq);
    if (state) {
      summary = { branch: state.branch, head: state.head, changes: state.files.length };
      codeStale = true;
    } else if (!seq) void refreshSummary();
  }
  $effect(() => {
    if (view === "code" && codeStale) {
      codeStale = false;
      codePulse++;
    }
  });
  async function refreshSummary() {
    if (!repo) return;
    try {
      summary = await api("/api/summary");
    } catch {}
  }
  // The open file goes in the address bar (replace, not push: a file is not a page of history).
  function located(path: string, line: number) {
    location = { path, line };
    if (!repo) return;
    const url = `/r/${encodeURIComponent(repo.id)}/` + (path ? "#" + encodeURI(path) + (line > 1 ? ":" + line : "") : "");
    if (url !== window.location.pathname + window.location.hash) history.replaceState(null, "", url);
  }

  // Git changes (commits, checkouts, staging from a terminal) arrive as they happen.
  $effect(() => {
    if (!repo) return;
    // Git changes arrive on the page's one connection (see socket.svelte.ts).
    connect(repo.id);
    followProblems();
    // On (re)connecting, look again: changes made while the page loaded or was disconnected are not
    // events (the server's watch began after them).
    const offHello = on("hello", () => {
      gitPulse++;
      if (view === "code") codePulse++;
      else codeStale = true;
      void refreshSummary();
    });
    const offGit = on("git", (m) => {
      const n = Number(m.n);
      // A change this tab's own action made was already answered with the new state; the answer may
      // still be on its way, so the event waits a moment before it counts.
      setTimeout(() => {
        if (n && n <= ownSeq) return;
        gitPulse++;
        // The file tree (its change marks) reloads when it is next shown, not behind Source Control.
        if (view === "code") codePulse++;
        else codeStale = true;
        void refreshSummary();
      }, 200);
    });
    return () => {
      offHello();
      offGit();
    };
  });

  async function login(e: SubmitEvent) {
    e.preventDefault();
    try {
      await api("/api/login", { method: "POST", body: JSON.stringify({ token }) });
      await start();
    } catch (err) {
      notify((err as Error).message);
    }
  }
  async function start() {
    await loadRepos();
    loaded = true;
    if (!authenticated) return;
    const at = parse();
    let id = at.id;
    if (!id) {
      let last = "";
      try {
        last = localStorage.getItem("falcon.last") || "";
      } catch {}
      id = repos.find((r) => r.id === last)?.id || (repos.length === 1 ? repos[0].id : "");
    }
    await enter(id, !at.id && !!id, at.path, at.line);
    if (!at.id && id) history.replaceState(null, "", `/r/${encodeURIComponent(id)}/`);
  }
  function showSide(which: "explorer" | "search" | "problems") {
    if (view === "code" && side === which) side = "";
    else {
      side = which;
      view = "code";
    }
  }
  onMount(() => {
    void start();
    const back = () => {
      const at = parse();
      if ((repo?.id || "") !== at.id) void enter(at.id, false, at.path, at.line);
      else if (at.path && at.path !== location.path) code?.openFile(at.path, at.line);
    };
    const focus = () => document.visibilityState === "visible" && void refreshSummary();
    window.addEventListener("popstate", back);
    window.addEventListener("focus", focus);
    return () => {
      window.removeEventListener("popstate", back);
      window.removeEventListener("focus", focus);
    };
  });
  const home = (p: string) => p.replace(/^\/(Users|home)\/[^/]+/, "~").replace(/^[A-Za-z]:\\Users\\[^\\]+/, "~");
</script>

<svelte:window
  onkeydown={(e) => {
    const mod = e.ctrlKey;
    // Ctrl+` shows or hides the terminal panel (focusing it); Ctrl+Shift+` opens a new terminal.
    if (mod && !e.altKey && (e.key === "`" || e.key === "~" || e.code === "Backquote") && repo) {
      e.preventDefault();
      if (e.shiftKey) void newTerminal();
      else if (terminalOpen) terminalOpen = false;
      else void showTerminal();
      return;
    }
    const k = e.key.toLowerCase();
    if (mod && k === "k") {
      e.preventDefault();
      quickOpen = false;
      foldersOpen = !foldersOpen;
    } else if (mod && !e.shiftKey && k === "p" && repo) {
      e.preventDefault();
      foldersOpen = false;
      quickStart = "";
      quickOpen = !quickOpen;
    } else if (((mod && !e.shiftKey && k === "t") || (mod && e.shiftKey && k === "o")) && repo) {
      // Go to symbol: Ctrl+Shift+O (browsers keep Ctrl+T for a new tab; it works where they pass it on).
      e.preventDefault();
      foldersOpen = false;
      quickStart = "#";
      quickOpen = true;
    } else if (mod && !e.shiftKey && k === "f" && repo && view === "code" && code?.find()) {
      e.preventDefault();
    } else if (mod && e.shiftKey && k === "f" && repo) {
      e.preventDefault();
      side = "search";
      view = "code";
    } else if (mod && e.shiftKey && k === "m" && repo) {
      e.preventDefault();
      showSide("problems");
    } else if (mod && e.shiftKey && k === "e" && repo) {
      e.preventDefault();
      showSide("explorer");
    } else if (e.ctrlKey && e.shiftKey && k === "g" && repo) {
      e.preventDefault();
      view = view === "git" ? "code" : "git";
    } else if (mod && k === "b" && repo && view === "code") {
      e.preventDefault();
      side = side ? "" : "explorer";
    }
  }}
  onbeforeunload={(e) => {
    if (dirty) e.preventDefault();
  }}
/>

{#if !authenticated}<main class="login-screen" in:fade>
    <Mark size={48} mode="intro" label="Falcon" />
    <h1>Falcon</h1>
    <form onsubmit={login} in:fly={{ y: 10, delay: 200 }}>
      <!-- svelte-ignore a11y_autofocus -->
      <input type="password" bind:value={token} placeholder="Access token" aria-label="Access token" autofocus />
      <button class="button primary">Open</button>
    </form>
  </main>
{:else}
  <div class="falcon" class:git-view={view === "git"}>
    <nav class="activity" aria-label="Views">
      <button class="activity-mark" title="All folders (Ctrl+K)" aria-label="All folders" onclick={() => (foldersOpen = true)}><Mark size={24} mode={working ? "working" : arrived ? "idle" : "intro"} label="Falcon" /></button>
      {#if repo}
        <button class:active={view === "code" && side === "explorer"} title="Explorer (Ctrl+Shift+E)" aria-label="Explorer" onclick={() => showSide("explorer")}><Files size={20} /></button>
        <button title="Go to file (Ctrl+P)" aria-label="Go to file" onclick={() => ((quickStart = ""), (quickOpen = true))}><FileSearch size={20} /></button>
        <button class:active={view === "code" && side === "search"} title="Search (Ctrl+Shift+F)" aria-label="Search" onclick={() => showSide("search")}><Search size={20} /></button>
        <button class:active={view === "code" && side === "problems"} title="Problems (Ctrl+Shift+M)" aria-label="Problems" onclick={() => showSide("problems")}
          ><ListTree size={20} />{#if problems.errors}<span class="activity-badge error" in:fly={{ y: -3, duration: 160 }}>{problems.errors > 99 ? "99+" : problems.errors}</span>{/if}</button
        >
        <button class:active={view === "git"} title="Source Control (Ctrl+Shift+G)" aria-label="Source Control" onclick={() => (view = view === "git" ? "code" : "git")}
          ><GitBranch size={20} />{#if summary?.changes}<span class="activity-badge" in:fly={{ y: -3, duration: 160 }}>{summary.changes > 99 ? "99+" : summary.changes}</span>{/if}</button
        >
      {/if}
      <span class="activity-gap"></span>
      <button title="Add a folder" aria-label="Add a folder" onclick={() => (adding = true)}><Plus size={20} /></button>
      <button
        title={`Theme: ${theme === "system" ? "follows the system" : theme} (click to change)`}
        aria-label={`Theme: ${theme}`}
        onclick={cycleTheme}
        >{#if theme === "light"}<Sun size={19} />{:else if theme === "dark"}<Moon size={19} />{:else}<SunMoon size={19} />{/if}</button
      >
    </nav>
    <main class="stage">
      {#if !loaded}<div class="loading-screen" out:fade={{ duration: 120 }}><Mark size={56} mode="working" label="Loading Falcon" /></div>
      {:else if repo}{#key repo.id}<div class="stage-view" in:fade={{ duration: 160 }}>
            <div class="stage-main">
            <div class="view-pane" class:hidden={view !== "code"}>
              <Code
                bind:this={code}
                repoID={repo.id}
                repoName={repo.name}
                {side}
                initialPath={initial.path}
                initialLine={initial.line}
                onerror={notify}
                ondirty={(d) => (dirty = d)}
                onlocation={located}
                pulse={codePulse}
              />
            </div>
            {#if view === "git"}<div class="view-pane" in:fade={{ duration: 140 }}>
                <GitClient
                  sequence={gitPulse}
                  repoID={repo.id}
                  onchange={gitAnswered}
                  onopen={async (path) => {
                    view = "code";
                    await tick();
                    code?.openFile(path);
                  }}
                />
              </div>{/if}
            </div>
            {#if terminalOpen && Panel}<Panel
                bind:this={panel}
                bind:height={terminalHeight}
                bind:maximized={terminalMax}
                repoID={repo.id}
                folder={repo.path}
                onopenfile={async (path: string, line: number, col: number) => {
                  view = "code";
                  await tick();
                  code?.openFile(path, line, col, 0);
                }}
                onhide={() => (terminalOpen = false)}
                onerror={notify}
              />{/if}
          </div>{/key}
      {:else}<div class="welcome" in:fade={{ duration: 200 }}>
          <div class="welcome-head" in:fly={{ y: 10, duration: 400 }}>
            <Mark size={64} mode="intro" label="Falcon" />
            <h1>Falcon</h1>
            <p>Fast, offline code reading and editing with Git, for every repository on this machine.</p>
          </div>
          {#if repos.length}<div class="welcome-list">
              {#each repos as r, i (r.id)}<a class="welcome-folder" href={`/r/${encodeURIComponent(r.id)}/`} in:fly={{ y: 10, delay: 80 + i * 40, duration: 320 }}
                  onclick={(e) => {
                    if (e.ctrlKey || e.button === 1) return;
                    e.preventDefault();
                    void enter(r.id);
                  }}
                  ><FolderGit2 size={18} /><span><strong>{r.name}</strong><small>{home(r.path)}</small></span>{#if r.branch}<em><GitBranch size={11} />{r.branch}</em>{/if}</a
                >{/each}
            </div>{/if}
          <button class="button primary welcome-add" onclick={() => (adding = true)} in:fly={{ y: 10, delay: 120 + repos.length * 40 }}><Plus size={15} />Add a folder</button>
          <p class="welcome-hint">Or start Falcon with folders: <code>falcon ~/code/app ~/code/api</code></p>
        </div>{/if}
    </main>
    <footer class="statusbar">
      {#if repo}<button class="status-item status-folder" onclick={() => (foldersOpen = true)} title={`${home(repo.path)}: switch folder (Ctrl+K)`} aria-label="Switch folder"
          ><FolderGit2 size={12} /><strong>{repo.name}</strong><ChevronsUpDown size={11} /></button
        ><button class="status-item mobile-only" onclick={() => ((quickStart = ""), (quickOpen = true))} aria-label="Go to file"><FileSearch size={12} /></button
        ><button class="status-item" class:on={terminalOpen} onclick={() => (terminalOpen ? (terminalOpen = false) : void showTerminal())} title="Terminal (Ctrl+`)" aria-label="Terminal"><SquareTerminal size={12} /><span class="wide-only">Terminal</span></button
        ><button class="status-item" onclick={() => showSide("problems")} title="Problems (Ctrl+Shift+M)" aria-label="Problems"
          ><CircleX size={12} />{problems.errors}<TriangleAlert size={12} />{problems.warnings}</button
        >{#if summary?.branch}<button class="status-item" onclick={() => (view = "git")} title="Source Control"
            ><GitBranch size={12} />{summary.branch}{#if summary.changes}<span class="status-changes">· {summary.changes} changed</span>{/if}</button
          >{/if}{#if location.path}<span class="status-item subtle wide-only">{location.path}{location.line ? ":" + location.line : ""}</span>{/if}{/if}
      <span class="status-gap"></span>
      <span class="status-item subtle wide-only">{repos.length} {repos.length === 1 ? "folder" : "folders"}</span>
      <span class="status-item subtle wide-only">Falcon {version}</span>
    </footer>
  </div>
{/if}

{#if quickOpen && repo}<QuickOpen
    initial={quickStart}
    onopen={async (path, line) => {
      view = "code";
      await tick();
      code?.openFile(path, line);
    }}
    onclose={() => (quickOpen = false)}
  />{/if}
{#if foldersOpen}<Folders
    {repos}
    current={repo?.id || ""}
    onswitch={switchTo}
    onadd={() => {
      foldersOpen = false;
      adding = true;
    }}
    onremove={removeFolder}
    onclose={() => (foldersOpen = false)}
  />{/if}
{#if adding}<AddFolder onadded={added} onclose={() => (adding = false)} />{/if}
{#if toast}<div class="toast" role="status" transition:fly={{ y: 12, duration: 220 }}>{toast}</div>{/if}
<ConfirmDialog />
