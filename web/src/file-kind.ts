// What a file is, for its icon and for which viewer opens it. Pure data: no network, no assets.

export type Viewer = "code" | "markdown" | "image" | "svg" | "pdf" | "video" | "audio" | "table" | "font" | "binary";

export type Kind = {
  /** Icon name (see FileIcon.svelte). */
  icon: string;
  /** Tint token: one of the --ic-* colours. */
  tint: string;
  viewer: Viewer;
};

const lang = (tint: string, icon = "code"): Kind => ({ icon, tint, viewer: "code" });

const byExt: Record<string, Kind> = {
  go: lang("cyan"),
  ts: lang("blue"), tsx: lang("blue"), mts: lang("blue"), cts: lang("blue"),
  js: lang("yellow"), jsx: lang("yellow"), mjs: lang("yellow"), cjs: lang("yellow"),
  py: lang("blue"), pyi: lang("blue"), ipynb: lang("orange"),
  rs: lang("orange"), java: lang("red"), kt: lang("purple"), kts: lang("purple"), scala: lang("red"),
  c: lang("indigo"), h: lang("indigo"), cc: lang("indigo"), cpp: lang("indigo"), cxx: lang("indigo"), hpp: lang("indigo"), hh: lang("indigo"),
  cs: lang("green"), swift: lang("orange"), rb: lang("red"), php: lang("purple"), dart: lang("cyan"), lua: lang("indigo"), zig: lang("orange"),
  ex: lang("purple"), exs: lang("purple"), erl: lang("red"), hs: lang("purple"), ml: lang("orange"), clj: lang("green"), r: lang("blue"), jl: lang("purple"),
  svelte: lang("orange"), vue: lang("green"), astro: lang("orange"),
  html: lang("orange"), htm: lang("orange"), xml: lang("orange"),
  css: lang("blue", "palette"), scss: lang("pink", "palette"), sass: lang("pink", "palette"), less: lang("indigo", "palette"),
  sh: lang("green", "terminal"), bash: lang("green", "terminal"), zsh: lang("green", "terminal"), fish: lang("green", "terminal"), ps1: lang("blue", "terminal"), bat: lang("gray", "terminal"),
  sql: lang("yellow", "database"), graphql: lang("pink", "braces"), gql: lang("pink", "braces"), proto: lang("cyan", "braces"),
  json: lang("yellow", "braces"), jsonc: lang("yellow", "braces"), json5: lang("yellow", "braces"),
  yaml: lang("purple", "config"), yml: lang("purple", "config"), toml: lang("gray", "config"), ini: lang("gray", "config"), conf: lang("gray", "config"), cfg: lang("gray", "config"), env: lang("yellow", "key"), properties: lang("gray", "config"),
  lock: lang("gray", "lock"), sum: lang("gray", "lock"),
  md: { icon: "markdown", tint: "accent", viewer: "markdown" }, markdown: { icon: "markdown", tint: "accent", viewer: "markdown" }, mdx: { icon: "markdown", tint: "accent", viewer: "markdown" },
  txt: lang("gray", "text"), rst: lang("gray", "text"), log: lang("gray", "text"), adoc: lang("gray", "text"),
  csv: { icon: "table", tint: "green", viewer: "table" }, tsv: { icon: "table", tint: "green", viewer: "table" },
  png: { icon: "image", tint: "pink", viewer: "image" }, jpg: { icon: "image", tint: "pink", viewer: "image" }, jpeg: { icon: "image", tint: "pink", viewer: "image" },
  gif: { icon: "image", tint: "pink", viewer: "image" }, webp: { icon: "image", tint: "pink", viewer: "image" }, avif: { icon: "image", tint: "pink", viewer: "image" },
  bmp: { icon: "image", tint: "pink", viewer: "image" }, ico: { icon: "image", tint: "pink", viewer: "image" },
  svg: { icon: "image", tint: "orange", viewer: "svg" },
  pdf: { icon: "pdf", tint: "red", viewer: "pdf" },
  mp4: { icon: "video", tint: "purple", viewer: "video" }, webm: { icon: "video", tint: "purple", viewer: "video" }, mov: { icon: "video", tint: "purple", viewer: "video" }, m4v: { icon: "video", tint: "purple", viewer: "video" },
  mp3: { icon: "audio", tint: "cyan", viewer: "audio" }, wav: { icon: "audio", tint: "cyan", viewer: "audio" }, ogg: { icon: "audio", tint: "cyan", viewer: "audio" }, flac: { icon: "audio", tint: "cyan", viewer: "audio" }, m4a: { icon: "audio", tint: "cyan", viewer: "audio" },
  ttf: { icon: "font", tint: "gray", viewer: "font" }, otf: { icon: "font", tint: "gray", viewer: "font" }, woff: { icon: "font", tint: "gray", viewer: "font" }, woff2: { icon: "font", tint: "gray", viewer: "font" },
  zip: { icon: "archive", tint: "yellow", viewer: "binary" }, gz: { icon: "archive", tint: "yellow", viewer: "binary" }, tgz: { icon: "archive", tint: "yellow", viewer: "binary" },
  tar: { icon: "archive", tint: "yellow", viewer: "binary" }, xz: { icon: "archive", tint: "yellow", viewer: "binary" }, "7z": { icon: "archive", tint: "yellow", viewer: "binary" }, rar: { icon: "archive", tint: "yellow", viewer: "binary" },
  db: { icon: "database", tint: "yellow", viewer: "binary" }, sqlite: { icon: "database", tint: "yellow", viewer: "binary" }, wasm: { icon: "binary", tint: "purple", viewer: "binary" },
  exe: { icon: "binary", tint: "gray", viewer: "binary" }, dll: { icon: "binary", tint: "gray", viewer: "binary" }, so: { icon: "binary", tint: "gray", viewer: "binary" }, dylib: { icon: "binary", tint: "gray", viewer: "binary" }, bin: { icon: "binary", tint: "gray", viewer: "binary" },
};

const byName: Record<string, Kind> = {
  "dockerfile": lang("blue", "container"), "containerfile": lang("blue", "container"), "docker-compose.yml": lang("blue", "container"), "compose.yaml": lang("blue", "container"),
  "makefile": lang("orange", "hammer"), "justfile": lang("orange", "hammer"), "cmakelists.txt": lang("indigo", "hammer"), "build.gradle": lang("green", "hammer"),
  "package.json": lang("green", "package"), "go.mod": lang("cyan", "package"), "cargo.toml": lang("orange", "package"), "pyproject.toml": lang("blue", "package"), "gemfile": lang("red", "package"), "pom.xml": lang("red", "package"),
  "license": lang("yellow", "license"), "license.md": lang("yellow", "license"), "license.txt": lang("yellow", "license"), "copying": lang("yellow", "license"),
  "readme.md": { icon: "readme", tint: "accent", viewer: "markdown" }, "readme": { icon: "readme", tint: "accent", viewer: "code" },
  ".gitignore": lang("orange", "git"), ".gitattributes": lang("orange", "git"), ".gitmodules": lang("orange", "git"),
  ".editorconfig": lang("gray", "config"), ".prettierrc": lang("gray", "config"), ".npmrc": lang("gray", "config"), ".env": lang("yellow", "key"),
};

const plain: Kind = { icon: "file", tint: "gray", viewer: "code" };

/** What a file is, from its name. */
export function kindOf(path: string): Kind {
  const name = path.split("/").at(-1)!.toLowerCase();
  if (byName[name]) return byName[name];
  if (name.startsWith(".env")) return byName[".env"];
  if (/(_test\.go|\.(test|spec)\.[cm]?[jt]sx?|_test\.py|^test_.*\.py)$/.test(name)) return { icon: "test", tint: "green", viewer: "code" };
  const parts = name.split(".");
  for (let i = 1; i < parts.length; i++) {
    const hit = byExt[parts.slice(i).join(".")];
    if (hit) return hit;
  }
  return plain;
}

/** A folder's icon. */
export function folderKind(path: string): { icon: string; tint: string } {
  const name = path.split("/").at(-1)!.toLowerCase();
  if (name === ".github" || name === ".git" || name === ".gitlab") return { icon: "folder-git", tint: "orange" };
  if (["src", "lib", "internal", "pkg", "app", "cmd"].includes(name)) return { icon: "folder-code", tint: "accent" };
  if (["test", "tests", "__tests__", "spec", "e2e", "testdata"].includes(name)) return { icon: "folder-test", tint: "green" };
  if (["config", ".config", "configs", "settings", ".vscode", ".idea"].includes(name)) return { icon: "folder-config", tint: "gray" };
  if (["docs", "doc", "documentation"].includes(name)) return { icon: "folder-docs", tint: "blue" };
  if (["node_modules", "vendor", "third_party", ".venv", "venv", "dist", "build", "target", "out", "bin"].includes(name)) return { icon: "folder", tint: "muted" };
  return { icon: "folder", tint: "folder" };
}
