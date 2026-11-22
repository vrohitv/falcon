#!/usr/bin/env python3
"""Turns VS Code's Seti icon theme (vs-seti-icon-theme.json, MIT) into web/src/seti-icons.ts: for each
file name and extension, the font glyph and its colours in dark and light themes.

    python3 scripts/seti-icons.py path/to/vs-seti-icon-theme.json > web/src/seti-icons.ts

The theme maps some files by VS Code language id; those ids are given their usual extensions and file
names here (as VS Code's built-in languages declare them). As in VS Code, a file name wins over an
extension, a longer extension over a shorter one, and an extension the theme lists over one that comes
from a language."""
import json, sys

LANGUAGES = {
    "argdown": (["ad", "adown", "argdown", "argdn"], []),
    "bat": (["bat", "cmd"], []),
    "bicep": (["bicep"], []),
    "blade": (["blade.php"], []),
    "c": (["c", "i"], []),
    "clojure": (["clj", "cljs", "cljc", "cljx", "clojure", "edn"], []),
    "coffeescript": (["coffee", "cson", "iced"], []),
    "cpp": (["cpp", "cc", "cxx", "c++", "hh", "hpp", "hxx", "h++", "ino", "inl", "ipp", "tcc", "tpp", "ixx", "cppm"], []),
    "csharp": (["cs", "csx", "cake"], []),
    "css": (["css"], []),
    "cuda-cpp": (["cu", "cuh"], []),
    "dart": (["dart"], []),
    "dockercompose": ([], ["docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"]),
    "dockerfile": (["dockerfile", "containerfile"], ["dockerfile", "containerfile"]),
    "elixir": (["ex", "exs"], []),
    "elm": (["elm"], []),
    "erb": (["erb", "rhtml", "html.erb"], []),
    "fsharp": (["fs", "fsi", "fsx", "fsscript"], []),
    "git-commit": ([], ["commit_editmsg", "merge_msg"]),
    "go": (["go"], ["go.mod", "go.sum", "go.work", "go.work.sum"]),
    "gradle": (["gradle"], []),
    "groovy": (["groovy", "gvy", "gy", "gsh"], []),
    "haml": (["haml"], []),
    "handlebars": (["handlebars", "hbs", "hjs"], []),
    "haskell": (["hs", "lhs"], []),
    "haxe": (["hx"], []),
    "html": (["html", "htm", "shtml", "xhtml", "xht", "mdoc", "jsp", "asp", "aspx", "jshtm", "volt"], []),
    "ignore": (["gitignore_global", "gitignore", "git-blame-ignore-revs"], [".gitignore"]),
    "jade": (["pug", "jade"], []),
    "java": (["java", "jav"], []),
    "javascript": (["js", "es6", "mjs", "cjs", "pac"], []),
    "javascriptreact": (["jsx"], []),
    "json": (["json", "webmanifest", "har", "jsonld", "geojson", "vuerc"], []),
    "jsonc": (["jsonc", "jsfmtrc", "swcrc", "hintrc"], []),
    "jsonl": (["jsonl", "ndjson"], []),
    "julia": (["jl"], []),
    "kotlin": (["kt", "kts"], []),
    "latex": (["latex", "ltx", "ctx"], []),
    "less": (["less"], []),
    "lua": (["lua"], []),
    "makefile": (["mak", "mk"], ["makefile", "gnumakefile", "ocamlmakefile"]),
    "markdown": (["md", "mkd", "mdwn", "mdown", "markdown", "markdn", "mdtxt", "mdtext", "workbook"], []),
    "objective-c": (["m"], []),
    "objective-cpp": (["mm"], []),
    "ocaml": (["ml", "mli"], []),
    "perl": (["pl", "pm", "pod", "t", "psgi"], []),
    "php": (["php", "php4", "php5", "phtml", "ctp"], []),
    "postcss": (["pcss", "postcss"], []),
    "powershell": (["ps1", "psm1", "psd1", "pssc", "psrc"], []),
    "properties": (["properties", "cfg", "conf", "directory", "editorconfig", "repo", "ini"], []),
    "python": (["py", "rpy", "pyw", "cpy", "gyp", "gypi", "pyi", "ipy", "pyt"], []),
    "r": (["r", "rhistory", "rprofile", "rt"], []),
    "razor": (["cshtml", "razor"], []),
    "ruby": (["rb", "rbx", "rjs", "gemspec", "rake", "ru", "podspec", "rbi"], ["gemfile", "rakefile", "podfile", "brewfile"]),
    "rust": (["rs"], []),
    "sass": (["sass"], []),
    "scss": (["scss"], []),
    "shellscript": (["sh", "bash", "bashrc", "bash_aliases", "bash_profile", "bash_login", "ebuild", "profile", "bash_logout", "xprofile",
                     "zsh", "zshrc", "zprofile", "zlogin", "zlogout", "zshenv", "zsh-theme", "ksh", "csh", "cshrc", "tcshrc", "fish"], []),
    "sql": (["sql", "dsql"], []),
    "stylus": (["styl"], []),
    "swift": (["swift"], []),
    "terraform": (["tf"], []),
    "tex": (["tex", "sty"], []),
    "typescript": (["ts", "cts", "mts"], []),
    "typescriptreact": (["tsx"], []),
    "vala": (["vala", "vapi"], []),
    "vue": (["vue"], []),
    "xml": (["xml", "xsd", "atom", "axml", "axaml", "bpmn", "csproj", "dtd", "fsproj", "fxml", "iml", "nuspec", "opml", "plist", "props",
             "rss", "storyboard", "targets", "vbproj", "vcxproj", "wsdl", "xaml", "xib", "xlf", "xliff", "xul"], []),
    "yaml": (["yaml", "yml", "eyaml", "eyml", "cff"], []),
}

theme = json.load(open(sys.argv[1]))
defs = theme["iconDefinitions"]
light = theme.get("light", {})

def glyph(name):
    d = defs[name]
    return chr(int(d["fontCharacter"].lstrip("\\"), 16)), d.get("fontColor", "")

used = {}
def ref(dark, lite):
    key = dark.lstrip("_")
    if key not in used:
        ch, color = glyph(dark)
        _, lcolor = glyph(lite) if lite in defs else (ch, color)
        used[key] = (ch, color, lcolor)
    return key

names, exts = {}, {}
for section, out in (("fileNames", names), ("fileExtensions", exts)):
    for k, v in theme[section].items():
        out[k.lower()] = ref(v, light.get(section, {}).get(k, v))
for lang, (lexts, lnames) in LANGUAGES.items():
    if lang not in theme["languageIds"]:
        continue
    v = theme["languageIds"][lang]
    key = ref(v, light.get("languageIds", {}).get(lang, v))
    for e in lexts:
        exts.setdefault(e.lower(), key)
    for n in lnames:
        names.setdefault(n.lower(), key)
default = ref(theme["file"], light.get("file", theme["file"]))

def js(d):
    return "{" + ",".join(f"{json.dumps(k)}:{json.dumps(v)}" for k, v in sorted(d.items())) + "}"

print("// Generated by scripts/seti-icons.py from Visual Studio Code 1.95.0's Seti icon theme (MIT); the font and")
print("// both licences are in assets/seti. Do not edit by hand.")
print("/** Glyph (in the Seti font), colour in a dark theme, colour in a light theme. */")
print("export const glyphs: Record<string, [string, string, string]> = " + js({k: list(v) for k, v in used.items()}) + ";")
print("export const byName: Record<string, string> = " + js(names) + ";")
print("export const byExtension: Record<string, string> = " + js(exts) + ";")
print(f"export const fallback = {json.dumps(default)};")
