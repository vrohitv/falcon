<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { api, APIError, timeAgo } from "./api";
  import type {
    GitState,
    GitFile,
    GitDetail,
    GitCommit,
    GitResult,
    DiffData,
  } from "./git";
  import GitDiff from "./GitDiff.svelte";
  import FileTree from "./FileTree.svelte";
  import FileIcon from "./FileIcon.svelte";
  import type { TreeFile } from "./file-tree";
  import {
    ChevronDown,
    GitBranch,
    GitCommitHorizontal,
    Plus,
    Minus,
    RefreshCw,
    Check,
    Search,
    ArrowUpRight,
    ShieldAlert,
    History,
    ArrowDown,
    ArrowUp,
    CloudUpload,
    Ellipsis,
    LoaderCircle,
    Undo2,
    Archive,
    GitMerge,
    Trash2,
    Pencil,
  } from "@lucide/svelte";
  import { confirmDialog } from "./confirm.svelte";
  import { slide, fade } from "./motion";
  import { layout, type GraphRow } from "./graph";
  let {
    sequence = 0,
    repoID = "repository",
    onopen,
    onchange,
  }: {
    sequence?: number;
    repoID?: string;
    onopen: (path: string) => void;
    onchange: (state?: GitState, seq?: number) => void;
  } = $props();
  let repoState = $state<GitState | null>(null),
    tab = $state<"changes" | "history" | "branches" | "stashes">("changes");
  // What a running action is doing, for the toolbar ("Pushing…").
  let doing = $state(""),
    menuOpen = $state(false),
    amend = $state(false),
    switchAfterCreate = $state(true),
    renaming = $state(""),
    renameTo = $state(""),
    remoteName = $state("origin"),
    remoteURL = $state(""),
    stashMessage = $state(""),
    stashUntracked = $state(true);
  let busy = $state(false),
    loading = $state(true),
    error = $state(""),
    notice = $state("");
  let fileFilter = $state("");
  let message = $state(""),
    authorName = $state(""),
    authorEmail = $state(""),
    branchName = $state("");
  let detail = $state<GitDetail | null>(null),
    diff = $state<DiffData | null>(null),
    diffBusy = $state(false);
  let selection = $state<{
    path: string;
    area: "working" | "staged" | "commit";
    commit?: string;
  } | null>(null);
  let mounted = false,
    stateTicket = 0,
    selectionTicket = 0;
  let historyBusy = $state(false),
    historyTicket = 0;
  let controller: AbortController | undefined,
    refreshTimer: ReturnType<typeof setTimeout>;
  const staged = $derived(
    repoState?.files.filter((f) => f.index && !f.conflict) || [],
  );
  const working = $derived(
    repoState?.files.filter((f) => f.worktree && !f.conflict) || [],
  );
  const conflicts = $derived(repoState?.files.filter((f) => f.conflict) || []);
  const merging = $derived(
    ["MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"].includes(repoState?.operation || ""),
  );
  const rebasing = $derived((repoState?.operation || "").startsWith("rebase"));
  // Nothing staged but there are changes: the button commits them all (VS Code's smart commit).
  const commitAll = $derived(!staged.length && working.some((f) => !f.protected) && !merging);
  const canCommit = $derived(
    !!repoState &&
      !busy &&
      (!!staged.length || commitAll || amend || merging) &&
      (!!message.trim() || (merging && !!repoState.merge_message)) &&
      !conflicts.length &&
      !staged.some((f) => f.protected) &&
      !repoState.detached &&
      (!repoState.operation || merging),
  );
  // With nothing to commit, the main button moves what is committed: publish or sync.
  const syncNext = $derived.by(() => {
    const s = repoState;
    if (!s || s.detached || s.operation || !s.head || staged.length || commitAll || amend) return "";
    if (!s.remotes?.length) return "";
    if (!s.upstream) return "publish";
    return s.ahead || s.behind ? "sync" : "";
  });
  const verbs: { [key: string]: string } = {
    fetch: "Fetching…",
    pull: "Pulling…",
    push: "Pushing…",
    sync: "Syncing…",
    publish: "Publishing…",
    merge: "Merging…",
    commit: "Committing…",
    "stash.save": "Stashing…",
  };
  const draftKey = $derived("falcon.git-draft:" + repoID);
  function statusName(f: GitFile, area: string) {
    const code = area === "staged" ? f.index : f.worktree;
    return (
      (
        {
          M: "Modified",
          A: "Added",
          D: "Deleted",
          R: "Renamed",
          C: "Copied",
          "?": "Untracked",
          U: "Unmerged",
        } as { [key: string]: string }
      )[code] || code
    );
  }
  function treeFiles(files: GitFile[], area: string): TreeFile[] {
    return files.map((file) => ({
      path: file.path,
      oldPath: file.old_path,
      disabled: file.protected,
      status: file.protected
        ? "P"
        : file.conflict
          ? "U"
          : area === "staged" || area === "commit"
            ? file.index
            : file.worktree,
      description: statusName(file, area),
    }));
  }
  async function refresh(redraw = true) {
    const ticket = ++stateTicket;
    try {
      const next = await api<GitState>("/api/git/status");
      if (ticket !== stateTicket) return;
      const changed = next.revision !== repoState?.revision;
      if (!repoState) {
        // Git's own identity first; otherwise the author last used in Falcon.
        let kept: { name?: string; email?: string } = {};
        try {
          kept = JSON.parse(localStorage.getItem("falcon.author") || "{}");
        } catch {}
        authorName = next.author_name || kept.name || "";
        authorEmail = next.author_email || kept.email || "";
      }
      repoState = next;
      serverRevision = next.revision;
      if (redraw && changed && selection && selection.area !== "commit")
        await loadDiff(selection.path, selection.area);
    } catch (e) {
      error = (e as Error).message;
    } finally {
      if (ticket === stateTicket) loading = false;
    }
  }
  // The whole file around the changes, or just the changes (remembered).
  let wholeFile = $state(false);
  try {
    wholeFile = localStorage.getItem("falcon.diff.whole") === "1";
  } catch {}
  function setWhole(v: boolean) {
    wholeFile = v;
    try {
      localStorage.setItem("falcon.diff.whole", v ? "1" : "0");
    } catch {}
    if (selection) void loadDiff(selection.path, selection.area, selection.commit);
  }
  async function loadDiff(
    path: string,
    area: "working" | "staged" | "commit",
    commit?: string,
  ) {
    const ticket = ++selectionTicket;
    controller?.abort();
    controller = new AbortController();
    // Reloading the same file keeps its diff on screen until the new one arrives (no flicker).
    const same = selection?.path === path && selection?.area === area && selection?.commit === commit;
    selection = { path, area, commit };
    if (!same) diff = null;
    diffBusy = !same;
    error = "";
    try {
      const q = new URLSearchParams({ path, area });
      if (commit) q.set("commit", commit);
      if (wholeFile) q.set("context", "all");
      const result = await api<DiffData>("/api/git/diff?" + q, {
        signal: controller.signal,
      });
      if (ticket === selectionTicket) diff = result;
    } catch (e) {
      if (ticket === selectionTicket && (e as Error).name !== "AbortError")
        error = (e as Error).message;
    } finally {
      if (ticket === selectionTicket) diffBusy = false;
    }
  }
  // History: every branch (or only this one) as a graph, newest first.
  let allBranches = $state(true);
  try {
    allBranches = localStorage.getItem("falcon.history.all") !== "0";
  } catch {}
  type GraphCommitRow = GitCommit & { refs: string[] };
  let graph = $state<{ commits: GraphCommitRow[]; next: number }>({ commits: [], next: -1 });
  const lanes = $derived<GraphRow[]>(layout(graph.commits));
  async function history(more = false) {
    if (more && (historyBusy || graph.next < 0)) return;
    const ticket = ++historyTicket;
    historyBusy = true;
    try {
      const q = new URLSearchParams({ limit: "120", all: String(allBranches) });
      if (more) q.set("offset", String(graph.next));
      const next = await api<{ commits: GraphCommitRow[]; next: number }>("/api/git/graph?" + q);
      if (ticket !== historyTicket) return;
      graph = more ? { commits: [...graph.commits, ...next.commits], next: next.next } : next;
    } catch (e) {
      error = (e as Error).message;
    } finally {
      if (ticket === historyTicket) historyBusy = false;
    }
  }
  async function inspect(commit: GitCommit) {
    const ticket = ++selectionTicket;
    controller?.abort();
    selection = null;
    diff = null;
    diffBusy = true;
    detail = null;
    try {
      const next = await api<GitDetail>(
        "/api/git/commit?commit=" + commit.hash,
      );
      if (ticket !== selectionTicket) return;
      detail = next;
      const first = next.files.find((f) => !f.protected);
      if (first) await loadDiff(first.path, "commit", commit.hash);
    } catch (e) {
      error = (e as Error).message;
    } finally {
      if (ticket === selectionTicket) diffBusy = false;
    }
  }
  function changeTab(next: typeof tab) {
    tab = next;
    error = "";
    if (next === "history") void history();
  }
  async function receive(op: GitResult) {
    error = "";
    if (op.status === "failed") {
      error = op.error || "Git operation failed.";
      if (op.state) adopt(op.state);
      else await refresh(false);
      onchange(op.state, op.seq);
      return;
    }
    if (op.request.action === "commit" && /^[0-9a-f]{40,64}$/.test(op.result || "")) {
      notice = (amend ? "Amended " : "Committed ") + op.result?.slice(0, 8);
      try {
        localStorage.setItem("falcon.author", JSON.stringify({ name: authorName, email: authorEmail }));
      } catch {}
      message = "";
      amend = false;
      localStorage.removeItem(draftKey);
    } else if (op.request.action === "branch.create") {
      notice = "Created branch " + op.result;
      branchName = "";
      if (switchAfterCreate) {
        busy = false;
        await refresh(false);
        await action("branch.switch", { branch: op.result });
        return;
      }
    } else if (op.request.action === "branch.switch") {
      notice = "Switched to " + op.result;
    } else if (/conflict/i.test(op.result || "")) {
      // The merge banner says what to do; a success tick would say the opposite.
      notice = "";
      error = op.result || "";
    } else notice = op.result || "Git updated";
    // A hunk staged or discarded: stay on the file, with its diff as it is now.
    if ((op.request.action.startsWith("hunk.") || op.request.action.startsWith("lines.")) && selection) {
      const keep = selection;
      if (op.state) adopt(op.state);
      else await refresh(false);
      onchange(op.state, op.seq);
      const stillStaged = repoState?.files.some((f) => f.path === keep.path && (keep.area === "staged" ? f.index : f.worktree));
      if (stillStaged) await loadDiff(keep.path, keep.area);
      else {
        selection = null;
        diff = null;
      }
      return;
    }
    selection = null;
    detail = null;
    diff = null;
    selectionTicket++;
    controller?.abort();
    diffBusy = false;
    if (op.state) adopt(op.state);
    else await refresh(false);
    onchange(op.state, op.seq);
    if (tab === "history") await history();
  }
  // The state an action answered with: the panel shows it at once, with no second request.
  function adopt(next: GitState) {
    stateTicket++;
    repoState = next;
    serverRevision = next.revision;
    loading = false;
  }

  // Staging and unstaging are instant, as in VS Code: the file moves at once, the request follows, and
  // clicks made meanwhile queue behind it (each sent with the revision the previous one produced).
  let serverRevision = "";
  let queue: Promise<void> = Promise.resolve();
  let pending = $state(0);
  function quick(name: "stage" | "unstage", paths: string[]) {
    if (!repoState || busy || !paths.length) return;
    const set = new Set(paths);
    repoState = {
      ...repoState,
      files: repoState.files.map((f) => {
        if (!set.has(f.path) || f.protected || f.conflict) return f;
        if (name === "stage" && f.worktree)
          return { ...f, index: f.worktree === "?" ? "A" : f.worktree === "D" ? "D" : f.index === "A" ? "A" : "M", worktree: "" };
        if (name === "unstage" && f.index)
          return { ...f, worktree: f.index === "A" ? "?" : f.index === "D" ? "D" : "M", index: "" };
        return f;
      }),
    };
    error = "";
    notice = "";
    pending++;
    queue = queue.then(async () => {
      let result: GitResult | null = null;
      try {
        result = await api<GitResult>("/api/git/commands", {
          method: "POST",
          body: JSON.stringify({ id: crypto.randomUUID(), action: name, revision: serverRevision, paths }),
        });
        if (result.state) serverRevision = result.state.revision;
        if (result.status === "failed") error = result.error || "Git operation failed.";
      } catch (e) {
        error = (e as Error).message;
      } finally {
        pending--;
      }
      // Only the last answer is shown: earlier ones would undo the clicks still on their way.
      if (pending === 0) {
        if (result?.state) adopt(result.state);
        else await refresh(false);
        onchange(result?.state, result?.seq);
        if (selection && selection.area !== "commit") {
          const f = repoState?.files.find((x) => x.path === selection!.path);
          const area = f && (selection.area === "staged" ? (f.index ? "staged" : f.worktree ? "working" : "") : f.worktree ? "working" : f.index ? "staged" : "");
          if (area) await loadDiff(selection.path, area);
          else {
            selection = null;
            diff = null;
          }
        }
      } else if (result?.seq) onchange(undefined, result.seq);
    });
  }
  async function action(name: string, extra: { [key: string]: unknown } = {}, then = "") {
    if (!repoState || busy) return;
    // Staging still on its way goes first: a commit right after a click commits what was clicked.
    if (pending) {
      busy = true;
      await queue;
      busy = false;
      if (!repoState) return;
    }
    busy = true;
    doing = verbs[name] || "Working…";
    menuOpen = false;
    commitMenu = false;
    error = "";
    notice = "";
    let ok = false;
    try {
      // The ID makes a retried request (after a dropped response) return the first result instead of
      // running twice.
      const result = await api<GitResult>("/api/git/commands", {
        method: "POST",
        body: JSON.stringify({ id: crypto.randomUUID(), action: name, revision: repoState.revision, ...extra }),
      });
      await receive(result);
      ok = result.status === "succeeded";
    } catch (e) {
      error = (e as Error).message;
      await refresh(false);
    } finally {
      busy = false;
      doing = "";
    }
    // Commit & Push, Commit & Sync: the second step once the commit is in (publishing a new branch).
    if (ok && then) await action(then === "push" && !repoState?.upstream ? "publish" : then);
  }
  // Discarding cannot be undone: say exactly what goes, then do it.
  async function discard(paths: string[] | "all") {
    const files = paths === "all" ? working.filter((f) => !f.protected) : working.filter((f) => paths.includes(f.path));
    if (!files.length) return;
    const fresh = files.filter((f) => f.worktree === "?").length;
    const one = files.length === 1 ? files[0].path : "";
    const what = one
      ? fresh
        ? `${one} is a new file: it will be deleted.`
        : `Your changes to ${one} will be lost.`
      : `Changes to ${files.length} files will be lost` + (fresh ? `, and ${fresh} new file${fresh === 1 ? "" : "s"} deleted.` : ".");
    if (!(await confirmDialog(what + " This cannot be undone.", { title: "Discard changes?", confirm: fresh && one ? "Delete file" : "Discard changes", danger: true })))
      return;
    await action(paths === "all" ? "discard.all" : "discard", paths === "all" ? {} : { paths });
  }
  async function undoCommit() {
    if (!repoState?.head) return;
    if (
      repoState.head_pushed &&
      !(await confirmDialog("This commit is already on " + repoState.upstream + ". Undoing it here means you'll have to force-push, or the next sync brings it back.", { title: "Undo a pushed commit?", confirm: "Undo commit" }))
    )
      return;
    const last = repoState.last_message;
    await action("undo");
    if (!error && !message.trim()) message = last;
  }
  // Commit, then optionally push (or publish a new branch) or sync, as VS Code's commit button offers.
  let commitMenu = $state(false);
  const canPush = $derived(!!repoState?.remotes?.length && !merging);
  function commit(then: "" | "push" | "sync" = "") {
    if (canCommit)
      void action(
        "commit",
        {
          message: message.trim() ? message : repoState?.merge_message || "",
          author_name: authorName,
          author_email: authorEmail,
          amend,
          all: commitAll && !amend,
        },
        then,
      );
    else if (!then && syncNext && !busy) void action(syncNext);
  }
  function toggleAmend() {
    amend = !amend;
    if (amend && !message.trim()) message = repoState?.last_message || "";
  }
  async function forcePush() {
    if (!(await confirmDialog(`Overwrite ${repoState?.upstream} with your branch? Commits on the remote that you don't have will be lost (if someone pushed since you last fetched, it stops instead).`, { title: "Force push?", confirm: "Force push", danger: true })))
      return;
    await action("push", { force: true });
  }
  async function deleteBranch(name: string) {
    if (!(await confirmDialog(`Delete the branch ${name}?`, { title: "Delete branch", confirm: "Delete", danger: true }))) return;
    await action("branch.delete", { branch: name });
    if (error.includes("not merged") && (await confirmDialog(`${name} has commits that are not on any other branch. Delete it anyway and lose them?`, { title: "Delete unmerged branch?", confirm: "Delete anyway", danger: true })))
      await action("branch.delete", { branch: name, force: true });
  }
  async function dropStash(ref: string) {
    if (!(await confirmDialog(`Drop ${ref}? Its changes will be lost.`, { title: "Drop stash", confirm: "Drop", danger: true }))) return;
    await action("stash.drop", { stash: ref });
  }
  // A success note fades after a few seconds; errors stay until dismissed.
  $effect(() => {
    if (!notice) return;
    const t = setTimeout(() => (notice = ""), 3500);
    return () => clearTimeout(t);
  });
  async function hunk(name: "hunk.stage" | "hunk.unstage" | "hunk.discard", id: string) {
    if (!selection) return;
    if (name === "hunk.discard" && !(await confirmDialog(`Discard this change to ${selection.path}? It cannot be undone.`, { title: "Discard hunk?", confirm: "Discard", danger: true })))
      return;
    await action(name, { paths: [selection.path], hunk: id });
  }
  async function abortOperation() {
    const what = rebasing ? "rebase" : repoState?.operation === "MERGE_HEAD" ? "merge" : "operation";
    if (!(await confirmDialog(`Abort the ${what}? Files go back to how they were before it started; resolutions you made are lost.`, { title: "Abort " + what + "?", confirm: "Abort " + what, danger: true })))
      return;
    await action("operation.abort");
  }
  function saveDraft() {
    try {
      localStorage.setItem(draftKey, message);
    } catch {}
  }
  $effect(() => {
    sequence;
    untrack(() => {
      if (!mounted) return;
      clearTimeout(refreshTimer);
      refreshTimer = setTimeout(() => {
        if (!busy && !pending) void refresh();
      }, 250);
    });
  });
  onMount(() => {
    mounted = true;
    try {
      message = localStorage.getItem(draftKey) || "";
    } catch {}
    void (async () => {
      await refresh();
    })();
    const focus = () => {
      if (!busy) void refresh();
    };
    window.addEventListener("focus", focus);
    return () => {
      mounted = false;
      stateTicket++;
      selectionTicket++;
      controller?.abort();
      clearTimeout(refreshTimer);
      window.removeEventListener("focus", focus);
    };
  });
</script>

<svelte:window
  onpointerdown={(e) => {
    if ((menuOpen || commitMenu) && !(e.target as Element).closest?.(".git-menu-wrap")) {
      menuOpen = false;
      commitMenu = false;
    }
  }}
  onkeydown={(e) => e.key === "Escape" && ((menuOpen = false), (commitMenu = false))}
/>
<section class="git-client" aria-label="Repository Git client">
  <header class="git-toolbar">
    <div>
      <GitBranch size={17} />
      <h1>Source Control</h1>
    </div>
    <span class="git-current-branch"
      >{repoState?.branch ||
        (repoState?.detached ? "Detached HEAD" : "Loading…")}</span
    >
    {#if repoState?.head}<code title={repoState.head}
        >{repoState.head.slice(0, 8)}</code
      >{/if}
    {#if repoState?.upstream}<span class="git-tracking" title={"Tracking " + repoState.upstream + (repoState.behind ? ` · ${repoState.behind} to pull` : "") + (repoState.ahead ? ` · ${repoState.ahead} to push` : "")}
        ><ArrowDown size={12} />{repoState.behind}<ArrowUp size={12} />{repoState.ahead}</span
      >{/if}
    <span class="git-toolbar-gap"></span>
    {#if doing}<span class="git-doing"><LoaderCircle size={13} class="spin" />{doing}</span>{/if}
    {#if repoState && !repoState.remotes?.length}<button class="button small-button" disabled={busy} onclick={() => changeTab("branches")} title="This repository has no remote; add one under Branches"
        >Add remote</button
      >{:else if repoState && !repoState.upstream && !repoState.detached && repoState.head}<button
        class="button small-button"
        disabled={busy || !!repoState.operation}
        onclick={() => action("publish")}><CloudUpload size={14} />Publish branch</button
      >{:else if repoState?.upstream}<button
        class="button small-button"
        title={`Pull ${repoState.behind} and push ${repoState.ahead} (${repoState.upstream})`}
        disabled={busy || !!repoState.operation}
        onclick={() => action("sync")}><RefreshCw size={13} />Sync{#if repoState.behind || repoState.ahead}&nbsp;{repoState.behind}↓ {repoState.ahead}↑{/if}</button
      >{/if}
    <span class="git-menu-wrap">
      <button class="icon-button" aria-label="More Git actions" aria-expanded={menuOpen} disabled={busy} onclick={() => (menuOpen = !menuOpen)}
        ><Ellipsis size={16} /></button
      >
      {#if menuOpen}<div class="git-menu" role="menu">
          {#if repoState?.remotes?.length}
            <button role="menuitem" onclick={() => action("fetch")}>Fetch</button>
            <button role="menuitem" disabled={!repoState.upstream} onclick={() => action("pull")}>Pull</button>
            <button role="menuitem" disabled={!repoState.upstream && !repoState.head} onclick={() => action("push")}>Push</button>
            <button role="menuitem" class="danger" disabled={!repoState.upstream} onclick={forcePush}>Force push (with lease)…</button>
            <hr />
          {/if}
          <button role="menuitem" disabled={!repoState?.head || !!repoState?.operation} onclick={() => { menuOpen = false; void undoCommit(); }}>Undo last commit</button>
          <button role="menuitem" disabled={!repoState?.head} onclick={() => { menuOpen = false; changeTab("changes"); toggleAmend(); }}>{amend ? "Stop amending" : "Amend last commit…"}</button>
          <hr />
          <button role="menuitem" disabled={repoState?.clean} onclick={() => action("stash.save", { include_untracked: true })}>Stash all changes</button>
          <button role="menuitem" disabled={!repoState?.stashes?.length} onclick={() => action("stash.pop", { stash: repoState!.stashes[0].ref })}>Pop latest stash</button>
          <hr />
          <button role="menuitem" disabled={!working.some((f) => !f.protected)} class="danger" onclick={() => { menuOpen = false; void discard("all"); }}>Discard all changes…</button>
        </div>{/if}
    </span>
    <button
      class="icon-button"
      aria-label="Refresh Git"
      disabled={busy}
      onclick={() => {
        void refresh();
        if (tab === "history") void history();
      }}><RefreshCw size={15} /></button
    >
  </header>
  {#if error}<div class="git-banner error" role="alert" transition:slide={{ x: 0, y: -6, duration: 180 }}>
      <ShieldAlert size={15} /><span>{error}</span
      ><button class="icon-button" aria-label="Dismiss" onclick={() => (error = "")}>×</button>
    </div>{/if}
  {#if notice}<div class="git-banner" role="status" transition:slide={{ x: 0, y: -6, duration: 180 }}>
      <Check size={14} />{notice}
    </div>{/if}
  {#if repoState?.operation}<div class="git-banner warn git-operation">
      <GitMerge size={15} /><span
        >{#if rebasing}Rebasing.{:else if repoState.operation === "MERGE_HEAD"}Merging into {repoState.branch}.{:else if repoState.operation === "CHERRY_PICK_HEAD"}Cherry-picking.{:else if repoState.operation === "REVERT_HEAD"}Reverting.{:else}{repoState.operation} in progress.{/if}
        {#if conflicts.length}{conflicts.length} conflicted file{conflicts.length === 1 ? "" : "s"}: open each and fix it (or keep one side), then mark it resolved.{:else if merging}No conflicts left: commit to finish.{:else if rebasing}No conflicts left: continue.{/if}</span
      >
      {#if rebasing}<button class="button small-button" disabled={busy || !!conflicts.length} onclick={() => action("operation.continue")}>Continue</button>{/if}
      {#if repoState.operation !== "BISECT_LOG"}<button class="button small-button" disabled={busy} onclick={abortOperation}>Abort</button>{/if}
    </div>{/if}
  <div class="git-layout">
    <aside class="git-sidebar">
      <div class="git-tabs" role="tablist" aria-label="Source Control sections">
        {#each [{ id: "changes", label: "Changes", count: repoState?.files.length || 0 }, { id: "history", label: "History", count: 0 }, { id: "branches", label: "Branches", count: 0 }, { id: "stashes", label: "Stashes", count: repoState?.stashes?.length || 0 }] as item}
          <button
            role="tab"
            aria-selected={tab === item.id}
            class:active={tab === item.id}
            aria-label={item.label}
            onclick={() => changeTab(item.id as typeof tab)}
            >{item.label}{#if item.count}<span class="git-tab-count">{item.count}</span>{/if}</button
          >
        {/each}
      </div>
      <div class="git-sidebar-body">
        {#if tab === "changes"}
          <form
            class="git-commit-form"
            onsubmit={(e) => {
              e.preventDefault();
              commit();
            }}
          >
            <label class="sr-only" for="git-commit-message"
              >Commit message</label
            >
            <textarea
              id="git-commit-message"
              bind:value={message}
              oninput={saveDraft}
              placeholder={merging && repoState?.merge_message ? repoState.merge_message : amend ? "Message for the amended commit" : "Commit message (Ctrl+Enter to commit)"}
              onkeydown={(e) => {
                if (e.ctrlKey && e.key === "Enter") {
                  e.preventDefault();
                  // Ctrl+Shift+Enter: commit and push.
                  if (e.shiftKey && canPush) commit("push");
                  else (e.currentTarget as HTMLTextAreaElement).form?.requestSubmit();
                }
              }}
              rows="3"
              disabled={busy}></textarea>
            {#if message.trim()}{@const subject = message.split("\n")[0].length}<span
                class="git-subject-length"
                class:long={subject > 50}
                class:too-long={subject > 72}
                title="The first line is the summary most tools show: about 50 characters reads best, 72 at most"
                >{subject}</span
              >{/if}
            <details class="git-author" open={!authorName || !authorEmail}>
              <summary
                >Author {#if authorName}<span>{authorName}</span>{/if}</summary
              >
              <label
                >Name<input
                  aria-label="Commit author name"
                  bind:value={authorName}
                  maxlength="200"
                /></label
              >
              <label
                >Email<input
                  aria-label="Commit author email"
                  type="email"
                  bind:value={authorEmail}
                  maxlength="320"
                /></label
              >
            </details>
            {#if syncNext && !canCommit && !message.trim()}<button class="button primary" disabled={busy}
                >{#if syncNext === "publish"}<CloudUpload size={15} />Publish branch{:else}<RefreshCw size={15} />Sync changes {repoState?.behind}↓ {repoState?.ahead}↑{/if}</button
              >{:else}<div class="git-commit-split">
                <button class="button primary" disabled={!canCommit}
                  ><GitCommitHorizontal size={15} />{#if merging}Commit merge{:else if amend}Amend last commit{:else if commitAll}{@const n = working.filter((f) => !f.protected).length}Commit {n === 1 ? "the change" : `all ${n} changes`}{:else}Commit {staged.length} staged {staged.length === 1 ? "file" : "files"}{/if}</button
                >{#if canPush}<span class="git-menu-wrap"
                    ><button type="button" class="button primary git-commit-more" aria-label="More commit actions" aria-expanded={commitMenu} disabled={!canCommit} onclick={() => (commitMenu = !commitMenu)}
                      ><ChevronDown size={14} /></button
                    >{#if commitMenu}<div class="git-menu" role="menu">
                        <button type="button" role="menuitem" onclick={() => commit("push")}
                          >{repoState?.upstream ? "Commit & Push" : "Commit & Publish branch"}<kbd>Ctrl+Shift+Enter</kbd></button
                        >
                        <button type="button" role="menuitem" disabled={!repoState?.upstream} onclick={() => commit("sync")}>Commit & Sync</button>
                      </div>{/if}</span
                  >{/if}
              </div>{/if}
            {#if repoState?.head && !merging}<label class="git-check"
                ><input type="checkbox" checked={amend} onchange={toggleAmend} disabled={busy} />Amend the last commit{#if amend && repoState.head_pushed}<em> — already pushed; you'll need to force-push</em>{/if}</label
              >{/if}
            {#if !repoState?.head && !loading}<p class="git-hint">
                First commit on {repoState?.branch}. Only staged files will be
                included.
              </p>{/if}
            {#if repoState?.detached}<p class="git-hint">
                Create and switch to a branch before committing.
              </p>{/if}
          </form>
          {#if repoState?.files.length}<div class="file-filter git-file-filter">
              <Search size={13} /><input
                aria-label="Filter changed files"
                placeholder="Filter changed files…"
                bind:value={fileFilter}
              />
            </div>{/if}
          {#if loading}<p class="git-hint">Reading repository…</p>
          {:else if repoState?.clean}<div class="git-clean">
              <Check size={22} /><strong>Working tree clean</strong>
              <p>No staged or unstaged changes.</p>
            </div>
          {:else}
            {#each [{ title: "Merge conflicts", files: conflicts, area: "working" }, { title: "Staged changes", files: staged, area: "staged" }, { title: "Changes", files: working, area: "working" }] as group}
              {#if group.files.length}
                <div class="git-group-heading">
                  <span>{group.title}</span><span class="git-count"
                    >{group.files.length}</span
                  >
                  {#if group.title === "Changes"}<button
                      class="icon-button"
                      aria-label="Discard all changes"
                      title="Discard all changes"
                      disabled={busy || group.files.every((f) => f.protected)}
                      onclick={() => discard("all")}><Undo2 size={14} /></button
                    >{/if}
                  {#if group.title !== "Merge conflicts"}<button
                      class="icon-button"
                      aria-label={group.area === "staged"
                        ? "Unstage all changes"
                        : "Stage all changes"}
                      disabled={busy ||
                        group.files.every((f) => f.protected)}
                      onclick={() =>
                        quick(
                          group.area === "staged" ? "unstage" : "stage",
                          group.files.filter((f) => !f.protected).map((f) => f.path),
                        )}
                    >
                      {#if group.area === "staged"}<Minus
                          size={14}
                        />{:else}<Plus size={14} />{/if}
                    </button>{/if}
                </div>
                <FileTree
                  files={treeFiles(group.files, group.area)}
                  label={group.title + " files"}
                  selected={selection?.area === group.area
                    ? selection.path
                    : ""}
                  filter={fileFilter}
                  storageKey={"falcon.tree.git:" + repoID + ":" + group.title}
                  open
                  action={group.title === "Merge conflicts" ? "Resolve" : group.area === "staged" ? "Unstage" : "Stage"}
                  ondiscard={group.title === "Changes" ? (paths) => void discard(paths) : undefined}
                  disabled={busy}
                  fileLabel={(path) =>
                    "View " + group.area + " changes for " + path}
                  onopen={(path) => {
                    detail = null;
                    void loadDiff(path, group.area as "working" | "staged");
                  }}
                  onaction={(paths) => quick(group.area === "staged" ? "unstage" : "stage", paths)}
                />
              {/if}
            {/each}
            {#if repoState?.files.some((f) => f.protected)}<p class="git-hint">
                P marks protected files. Their contents and staging are
                unavailable here.
              </p>{/if}
          {/if}
        {:else if tab === "history"}
          <div class="git-group-heading">
            <History size={13} /><span>History</span><span class="git-count">{graph.commits.length}{graph.next >= 0 ? "+" : ""}</span>
            <label class="git-check git-all" title="Show every branch, remote branch and tag"
              ><input
                type="checkbox"
                checked={allBranches}
                onchange={(e) => {
                  allBranches = (e.currentTarget as HTMLInputElement).checked;
                  try {
                    localStorage.setItem("falcon.history.all", allBranches ? "1" : "0");
                  } catch {}
                  void history();
                }}
              />All branches</label
            >
          </div>
          <div class="git-graph">
            {#each graph.commits as commit, i (commit.hash)}{@const g = lanes[i]}
              <button class="git-graph-row" class:active={detail?.commit.hash === commit.hash} onclick={() => inspect(commit)} title={commit.hash}>
                <svg class="git-lanes" width={Math.max(1, g.width) * 14 + 4} height="44" aria-hidden="true">
                  {#each g.up as e}<path class="lane-{e.lane % 8}" d={`M${7 + e.from * 14} 0 C ${7 + e.from * 14} 12, ${7 + e.to * 14} 10, ${7 + e.to * 14} 22`} />{/each}
                  {#each g.down as e}<path class="lane-{e.lane % 8}" d={`M${7 + e.from * 14} 22 C ${7 + e.from * 14} 34, ${7 + e.to * 14} 32, ${7 + e.to * 14} 44`} />{/each}
                  <circle class="dot lane-{g.lane % 8}" class:merge={commit.parents.length > 1} cx={7 + g.lane * 14} cy="22" r={commit.parents.length > 1 ? 3.5 : 4} />
                </svg>
                <span class="git-graph-text">
                  <span class="git-graph-subject"
                    >{#each commit.refs as ref}{@const head = ref.startsWith("HEAD -> ")}{@const tag = ref.startsWith("tag: ")}<span
                        class="git-ref"
                        class:head
                        class:tag
                        class:remote={!head && !tag && ref.includes("/")}>{head ? ref.slice(8) : tag ? ref.slice(5) : ref}</span
                      >{/each}<strong>{commit.subject || "(no subject)"}</strong></span
                  >
                  <span class="git-graph-meta"><code>{commit.hash.slice(0, 7)}</code>{commit.author}<time>{timeAgo(commit.date)}</time></span>
                </span>
              </button>
            {/each}
          </div>
          {#if !graph.commits.length && !historyBusy}<p class="git-hint">No commits yet. Stage your first files to create one.</p>{/if}
          {#if graph.next >= 0}<button class="button git-more" disabled={historyBusy} onclick={() => history(true)}
              >{historyBusy ? "Loading…" : "Load more commits"}</button
            >{/if}
        {:else if tab === "branches"}
          <form
            class="git-branch-form"
            onsubmit={(e) => {
              e.preventDefault();
              void action("branch.create", { branch: branchName });
            }}
          >
            <label for="git-branch-name">New branch name</label>
            <input
              id="git-branch-name"
              bind:value={branchName}
              placeholder="feature/my-change"
              disabled={busy}
            />
            <label class="git-check"><input type="checkbox" bind:checked={switchAfterCreate} />Switch to it</label>
            <button
              class="button"
              disabled={busy ||
                !branchName.trim() ||
                !repoState?.head}><Plus size={14} />Create branch</button
            >
            <p class="git-hint">
              From the current commit. Uncommitted changes come along when you switch.
            </p>
          </form>
          <div class="git-group-heading"><GitBranch size={13} /><span>Local branches</span><span class="git-count">{repoState?.branches.length || 0}</span></div>
          {#each repoState?.branches || [] as branch}
            <div class="git-branch-entry">
              <div>
                <GitBranch size={14} /><strong>{branch.name}</strong
                >{#if branch.current}<span class="git-tag">Current</span>{/if}
              </div>
              <span
                ><code>{branch.head.slice(0, 8)}</code>{branch.upstream ||
                  "Local branch"}</span
              >
              {#if renaming === branch.name}<form
                  class="git-inline-form"
                  onsubmit={(e) => {
                    e.preventDefault();
                    const to = renameTo.trim();
                    renaming = "";
                    if (to && to !== branch.name) void action("branch.rename", { branch: branch.name, new_name: to });
                  }}
                  ><input aria-label={"New name for " + branch.name} bind:value={renameTo} onkeydown={(e) => e.key === "Escape" && (renaming = "")} /><button class="button small-button">Rename</button></form
                >
              {:else}<div class="git-branch-actions">
                  {#if !branch.current}<button
                      class="button small-button"
                      aria-label={"Switch to " + branch.name}
                      disabled={busy || !!branch.worktree || !!repoState?.operation}
                      onclick={() => action("branch.switch", { branch: branch.name })}
                      >{branch.worktree ? "In another worktree" : "Switch"}</button
                    ><button
                      class="button small-button"
                      title={`Merge ${branch.name} into ${repoState?.branch}`}
                      disabled={busy || !!repoState?.operation || repoState?.detached}
                      onclick={() => action("merge", { branch: branch.name })}><GitMerge size={13} />Merge</button
                    >{/if}<button
                    class="icon-button"
                    aria-label={"Rename " + branch.name}
                    title="Rename"
                    disabled={busy}
                    onclick={() => {
                      renaming = branch.name;
                      renameTo = branch.name;
                    }}><Pencil size={13} /></button
                  >{#if !branch.current}<button
                      class="icon-button"
                      aria-label={"Delete " + branch.name}
                      title="Delete"
                      disabled={busy || !!branch.worktree}
                      onclick={() => deleteBranch(branch.name)}><Trash2 size={13} /></button
                    >{/if}
                </div>{/if}
            </div>
          {/each}
          {#if repoState?.remote_branches?.length}<div class="git-group-heading">
              <CloudUpload size={13} /><span>Remote branches</span><span class="git-count">{repoState.remote_branches.length}</span>
            </div>
            {#each repoState.remote_branches as remote}{@const local = repoState.branches.find((b) => b.upstream === remote)}
              <div class="git-branch-entry">
                <div><GitBranch size={14} /><strong>{remote}</strong>{#if local}<span class="git-tag">{local.name}</span>{/if}</div>
                <div class="git-branch-actions">
                  {#if !local}<button
                      class="button small-button"
                      title={`Create a local branch tracking ${remote} and switch to it`}
                      disabled={busy || !!repoState.operation}
                      onclick={() => action("branch.checkout", { branch: remote })}>Check out</button
                    >{/if}{#if repoState.upstream !== remote}<button
                      class="button small-button"
                      title={`Merge ${remote} into ${repoState.branch}`}
                      disabled={busy || !!repoState.operation || repoState.detached}
                      onclick={() => action("merge", { branch: remote })}><GitMerge size={13} />Merge</button
                    >{/if}
                </div>
              </div>
            {/each}{/if}
          <div class="git-group-heading"><span>Remotes</span><span class="git-count">{repoState?.remotes?.length || 0}</span></div>
          {#each repoState?.remotes || [] as remote}<div class="git-branch-entry">
              <div><strong>{remote.name}</strong></div>
              <span class="git-remote-url" title={remote.url}>{remote.url}</span>
            </div>{/each}
          <form
            class="git-branch-form"
            onsubmit={(e) => {
              e.preventDefault();
              void action("remote.add", { remote: remoteName.trim(), url: remoteURL.trim() }).then(() => {
                if (!error) remoteURL = "";
              });
            }}
          >
            <label for="git-remote-url">Add a remote</label>
            <div class="git-remote-row">
              <input aria-label="Remote name" bind:value={remoteName} placeholder="origin" disabled={busy} />
              <input id="git-remote-url" bind:value={remoteURL} placeholder="git@github.com:you/repo.git, https://…, or a folder" disabled={busy} />
            </div>
            <button class="button" disabled={busy || !remoteName.trim() || !remoteURL.trim()}><Plus size={14} />Add remote</button>
            <p class="git-hint">Push and pull use your own Git credentials (credential helper or SSH agent). A folder works offline.</p>
          </form>
        {:else if tab === "stashes"}
          <form
            class="git-branch-form"
            onsubmit={(e) => {
              e.preventDefault();
              void action("stash.save", { message: stashMessage, include_untracked: stashUntracked }).then(() => {
                if (!error) stashMessage = "";
              });
            }}
          >
            <label for="git-stash-message">Stash your changes</label>
            <input id="git-stash-message" bind:value={stashMessage} placeholder="What you were doing (optional)" disabled={busy} />
            <label class="git-check"><input type="checkbox" bind:checked={stashUntracked} />Include new (untracked) files</label>
            <button class="button" disabled={busy || !!repoState?.clean}><Archive size={14} />Stash changes</button>
            <p class="git-hint">Puts your changes aside and leaves the working tree clean; bring them back later.</p>
          </form>
          {#each repoState?.stashes || [] as st}<div class="git-branch-entry">
              <div><Archive size={14} /><strong>{st.message}</strong></div>
              <span><code>{st.ref}</code>{st.when}</span>
              <div class="git-branch-actions">
                <button class="button small-button" disabled={busy} title="Bring the changes back and remove the stash" onclick={() => action("stash.pop", { stash: st.ref })}>Pop</button>
                <button class="button small-button" disabled={busy} title="Bring the changes back and keep the stash" onclick={() => action("stash.apply", { stash: st.ref })}>Apply</button>
                <button class="icon-button" aria-label={"Drop " + st.ref} title="Drop" disabled={busy} onclick={() => dropStash(st.ref)}><Trash2 size={13} /></button>
              </div>
            </div>{/each}
          {#if !repoState?.stashes?.length}<p class="git-hint">No stashes.</p>{/if}
        {/if}
      </div>
    </aside>
    <main class="git-preview">
      {#if detail}<div class="git-commit-detail">
          <div>
            <GitCommitHorizontal size={16} />
            <h2>{detail.commit.subject}</h2>
            <code>{detail.commit.hash.slice(0, 12)}</code>
          </div>
          <p>
            {detail.commit.author} · {new Date(
              detail.commit.date,
            ).toLocaleString()}
          </p>
          {#if detail.commit.message.trim() !== detail.commit.subject}<pre>{detail.commit.message
                .slice(detail.commit.subject.length)
                .trim()}</pre>{/if}
          <FileTree
            files={treeFiles(detail.files, "commit")}
            label="Commit files"
            selected={selection?.area === "commit" ? selection.path : ""}
            storageKey={"falcon.tree.history:" + repoID}
            open
            maxHeight={196}
            onopen={(path) =>
              void loadDiff(path, "commit", detail!.commit.hash)}
          />
        </div>{/if}
      {#if selection}<div class="git-diff-toolbar">
          <FileIcon path={selection.path} /><strong>{selection.path}</strong
          ><span class="git-diff-comparison"
            >{conflicts.some((f) => f.path === selection?.path)
              ? "HEAD → Working tree (with conflict markers)"
              : selection.area === "staged"
              ? "HEAD → Index"
              : selection.area === "commit"
                ? "Parent → Commit"
                : "Index → Working tree"}</span
          >
          {#if conflicts.some((f) => f.path === selection?.path)}<span class="git-conflict-tools"
              ><button class="button small-button" disabled={busy} title="Keep your branch's version of the whole file" onclick={() => action("conflict.ours", { paths: [selection!.path] })}>Keep current</button
              ><button class="button small-button" disabled={busy} title="Take the incoming branch's version of the whole file" onclick={() => action("conflict.theirs", { paths: [selection!.path] })}>Take incoming</button
              ><button class="button small-button primary" disabled={busy} title="You fixed the file: stage it as resolved" onclick={() => action("stage", { paths: [selection!.path] })}>Mark resolved</button></span
            >{/if}
          {#if selection.area !== "commit"}<button
              class="icon-button"
              aria-label="Open selected source file"
              title="Open source"
              onclick={() => onopen(selection!.path)}
              ><ArrowUpRight size={15} /></button
            >{/if}
        </div>{/if}
      {#if diffBusy}<div class="git-preview-empty">Loading diff…</div>
      {:else if diff}{#key JSON.stringify(selection)}<GitDiff
            data={diff}
            area={selection?.area === "commit" || conflicts.some((f) => f.path === selection?.path) ? "" : selection?.area}
            {busy}
            full={wholeFile}
            onfull={setWhole}
            onhunk={hunk}
            onlines={(name, id, lines) => selection && action(name, { paths: [selection.path], hunk: id, lines })}
          />{/key}
      {:else if !detail}<div class="git-preview-empty">
          <GitBranch size={34} />
          <h2>Your changes, ready to review.</h2>
          <p>
            Select a file to inspect its diff, or browse a commit from history.
          </p>
          <span>Stage deliberately. Commit exactly what you reviewed.</span>
        </div>{/if}
    </main>
  </div>
</section>
