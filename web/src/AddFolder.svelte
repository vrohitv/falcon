<script lang="ts">
  // Add a folder: type a path, or walk the disk from your home folder. Repositories are marked; a plain
  // folder can be made one (git init) on the way in.
  import { api, type Repo } from "./api";
  import { fade, pop } from "./motion";
  import { Folder, FolderGit2, ArrowUp, LoaderCircle } from "@lucide/svelte";
  let {
    onadded,
    onclose,
  }: {
    onadded: (repo: Repo) => void;
    onclose: () => void;
  } = $props();
  type Entry = { name: string; path: string; git: boolean };
  let dir = $state(""),
    parent = $state(""),
    path = $state(""),
    folders = $state<Entry[]>([]),
    busy = $state(false),
    error = $state(""),
    notGit = $state(false);
  async function browse(to: string) {
    try {
      const r = await api<{ path: string; parent: string; folders: Entry[] }>("/api/browse?path=" + encodeURIComponent(to));
      dir = r.path;
      parent = r.parent;
      folders = r.folders;
      path = r.path;
      error = "";
      notGit = false;
    } catch (e) {
      error = (e as Error).message;
    }
  }
  async function add(target = path, init = false) {
    if (!target.trim() || busy) return;
    busy = true;
    error = "";
    try {
      const repo = await api<Repo>("/api/repos", { method: "POST", body: JSON.stringify({ path: target, init }) });
      onadded(repo);
    } catch (e) {
      error = (e as Error).message;
      notGit = /not a Git repository/.test(error);
    } finally {
      busy = false;
    }
  }
  $effect(() => {
    void browse("");
  });
  function focus(node: HTMLInputElement) {
    node.focus();
  }
</script>

<div class="palette-backdrop" role="presentation" onclick={onclose} transition:fade={{ duration: 120 }}></div>
<div class="palette add-folder" role="dialog" aria-label="Add a folder" transition:pop={{ duration: 180, start: 0.97 }}>
  <form
    onsubmit={(e) => {
      e.preventDefault();
      void add();
    }}
  >
    <input
      class="palette-input"
      use:focus
      bind:value={path}
      placeholder="~/code/project"
      aria-label="Folder path"
      onkeydown={(e) => e.key === "Escape" && onclose()}
    />
  </form>
  {#if error}<div class="add-error" in:fade={{ duration: 120 }}>
      {error}{#if notGit}<button class="button small-button" disabled={busy} onclick={() => add(path, true)}>Initialize Git here and add</button>{/if}
    </div>{/if}
  <div class="palette-results">
    {#if parent}<button class="browse-row" onclick={() => browse(parent)}><ArrowUp size={15} /><span>..</span></button>{/if}
    {#each folders as f (f.path)}<div class="browse-entry">
        <button class="browse-row" ondblclick={() => (f.git ? add(f.path) : browse(f.path))} onclick={() => browse(f.path)} title={f.path}
          >{#if f.git}<FolderGit2 size={15} class="git-folder" />{:else}<Folder size={15} />{/if}<span>{f.name}</span></button
        >{#if f.git}<button class="button small-button" disabled={busy} onclick={() => add(f.path)}>Add</button>{/if}
      </div>{/each}
    {#if !folders.length && !error}<p class="palette-empty">No folders here</p>{/if}
  </div>
  <div class="palette-footer">
    <span class="add-where">{dir}</span>
    <button class="button primary small-button" disabled={busy || !path.trim()} onclick={() => add()}
      >{#if busy}<LoaderCircle size={13} class="spin" />{/if}Add this folder</button
    >
  </div>
</div>
