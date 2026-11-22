<script lang="ts">
  // File icons as in VS Code's default theme: the Seti icon font, one glyph per language or kind of
  // file, in its colours (one set for dark themes, one for light). A glyph is a character, so a tree of
  // thousands of files costs no SVGs. Folders keep Falcon's line icons, tinted by kind.
  import { Folder, FolderOpen, FolderGit, FolderCode, FolderCog, FolderCheck, FolderOpenDot } from "@lucide/svelte";
  import { folderKind } from "./file-kind";
  import { glyphs, byName, byExtension, fallback } from "./seti-icons";

  /** The Seti glyph for a file: by its name, else its longest known extension, else the plain file. */
  function seti(path: string): string {
    const base = (path.split("/").pop() || "").toLowerCase();
    if (byName[base]) return byName[base];
    const parts = base.split(".");
    for (let i = 1; i < parts.length; i++) {
      const hit = byExtension[parts.slice(i).join(".")];
      if (hit) return hit;
    }
    return fallback;
  }
  let {
    path,
    size = 16,
    folder = false,
    open = false,
  }: { path: string; size?: number; folder?: boolean; open?: boolean } = $props();
  const icons: Record<string, typeof Folder> = {
    folder: Folder,
    "folder-open": FolderOpen,
    "folder-git": FolderGit,
    "folder-code": FolderCode,
    "folder-config": FolderCog,
    "folder-test": FolderCheck,
    "folder-docs": FolderOpenDot,
  };
  const kind = $derived.by(() => {
    const f = folderKind(path);
    return { icon: open && f.icon === "folder" ? "folder-open" : f.icon, tint: f.tint };
  });
  const Icon = $derived(icons[kind.icon] || Folder);
  const glyph = $derived(folder ? "" : seti(path));
  const g = $derived(glyph ? glyphs[glyph] || glyphs[fallback] : null);
</script>

{#if g}<span
    class="file-type-icon seti"
    data-icon={glyph}
    style:width="{size}px"
    style:height="{size}px"
    style:font-size="{Math.round(size * 1.3)}px"
    style:--seti-dark={g[1]}
    style:--seti-light={g[2]}
    aria-hidden="true">{g[0]}</span
  >{:else}<span class="file-type-icon" data-tint={kind.tint} data-icon={kind.icon} style:width="{size}px" style:height="{size}px" aria-hidden="true"
    ><Icon size={size} strokeWidth={1.75} /></span
  >{/if}

<style>
  @font-face {
    font-family: "falcon-seti";
    src: url("./assets/seti/seti.woff") format("woff");
    font-display: block;
  }
  .file-type-icon.seti {
    overflow: hidden;
    color: var(--seti-dark);
    font-family: "falcon-seti";
    font-style: normal;
    line-height: 1;
    -webkit-font-smoothing: antialiased;
  }
  :global(:root[data-theme="light"]) .file-type-icon.seti {
    color: var(--seti-light);
  }
  @media (prefers-color-scheme: light) {
    :global(:root:not([data-theme="dark"])) .file-type-icon.seti {
      color: var(--seti-light);
    }
  }
  .file-type-icon {
    display: inline-grid;
    place-items: center;
    flex-shrink: 0;
    vertical-align: middle;
    color: var(--ic-gray);
  }
  .file-type-icon[data-tint="blue"] { color: var(--ic-blue); }
  .file-type-icon[data-tint="cyan"] { color: var(--ic-cyan); }
  .file-type-icon[data-tint="green"] { color: var(--ic-green); }
  .file-type-icon[data-tint="yellow"] { color: var(--ic-yellow); }
  .file-type-icon[data-tint="orange"] { color: var(--ic-orange); }
  .file-type-icon[data-tint="red"] { color: var(--ic-red); }
  .file-type-icon[data-tint="pink"] { color: var(--ic-pink); }
  .file-type-icon[data-tint="purple"] { color: var(--ic-purple); }
  .file-type-icon[data-tint="indigo"] { color: var(--ic-indigo); }
  .file-type-icon[data-tint="accent"] { color: var(--accent); }
  .file-type-icon[data-tint="folder"] { color: var(--ic-folder); }
  .file-type-icon[data-tint="muted"] { color: var(--subtle); opacity: 0.75; }
</style>
