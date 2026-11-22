// What a file is, for which viewer opens it (file icons come from the Seti table, seti-icons.ts), and a
// folder's icon. Pure data: no network, no assets.

export type Viewer = "code" | "markdown" | "image" | "svg" | "pdf" | "video" | "audio" | "table" | "font" | "binary";

const viewers: Record<string, Viewer> = {};
const set = (viewer: Viewer, exts: string) => exts.split(" ").forEach((e) => (viewers[e] = viewer));
set("markdown", "md markdown mdx");
set("table", "csv tsv");
set("image", "png jpg jpeg gif webp avif bmp ico");
set("svg", "svg");
set("pdf", "pdf");
set("video", "mp4 webm mov m4v");
set("audio", "mp3 wav ogg flac m4a");
set("font", "ttf otf woff woff2");
set("binary", "zip gz tgz tar xz 7z rar db sqlite wasm exe dll so dylib bin");

/** What a file is, from its name: the viewer that opens it. */
export function kindOf(path: string): { viewer: Viewer } {
  const name = path.split("/").at(-1)!.toLowerCase();
  if (name === "readme") return { viewer: "code" };
  return { viewer: viewers[name.split(".").at(-1)!] || "code" };
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
