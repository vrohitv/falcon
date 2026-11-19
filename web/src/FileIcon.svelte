<script lang="ts">
  // File and folder icons from the same line-icon family as the rest of Falcon, tinted by language or kind
  // with colours that follow the theme.
  import {
    File,
    FileCode,
    FileText,
    FileImage,
    FilePlay,
    FileMusic,
    FileArchive,
    FileLock,
    FileTerminal,
    FileCog,
    FileSpreadsheet,
    FileType,
    FileBraces,
    FileKey,
    FileBox,
    FileDigit,
    FlaskConical,
    Database,
    Package,
    Container,
    Hammer,
    Scale,
    BookOpen,
    Palette,
    GitBranch,
    Folder,
    FolderOpen,
    FolderGit,
    FolderCode,
    FolderCog,
    FolderCheck,
    FolderOpenDot,
    FileChartColumn,
  } from "@lucide/svelte";
  import { kindOf, folderKind } from "./file-kind";
  let {
    path,
    size = 16,
    folder = false,
    open = false,
  }: { path: string; size?: number; folder?: boolean; open?: boolean } = $props();
  const icons: Record<string, typeof File> = {
    file: File,
    code: FileCode,
    text: FileText,
    markdown: FileText,
    readme: BookOpen,
    image: FileImage,
    video: FilePlay,
    audio: FileMusic,
    archive: FileArchive,
    lock: FileLock,
    terminal: FileTerminal,
    config: FileCog,
    table: FileSpreadsheet,
    font: FileType,
    braces: FileBraces,
    key: FileKey,
    binary: FileBox,
    pdf: FileChartColumn,
    digit: FileDigit,
    test: FlaskConical,
    database: Database,
    package: Package,
    container: Container,
    hammer: Hammer,
    license: Scale,
    palette: Palette,
    git: GitBranch,
    folder: Folder,
    "folder-open": FolderOpen,
    "folder-git": FolderGit,
    "folder-code": FolderCode,
    "folder-config": FolderCog,
    "folder-test": FolderCheck,
    "folder-docs": FolderOpenDot,
  };
  const kind = $derived.by(() => {
    if (folder) {
      const f = folderKind(path);
      return { icon: open && f.icon === "folder" ? "folder-open" : f.icon, tint: f.tint };
    }
    const k = kindOf(path);
    return { icon: k.icon, tint: k.tint };
  });
  const Icon = $derived(icons[kind.icon] || File);
</script>

<span class="file-type-icon" data-tint={kind.tint} data-icon={kind.icon} style:width="{size}px" style:height="{size}px" aria-hidden="true"
  ><Icon size={size} strokeWidth={1.75} /></span
>

<style>
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
