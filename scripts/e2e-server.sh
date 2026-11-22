#!/usr/bin/env bash
# Two repositories and a remote for the browser tests, served by one Falcon.
set -euo pipefail
project=$(cd "$(dirname "$0")/.." && pwd)
dir="$project/web/.e2e"
rm -rf "$dir"
mkdir -p "$dir"
mk() {
  git -C "$1" init -q -b main
  git -C "$1" config user.name 'Falcon Test'
  git -C "$1" config user.email 'test@example.test'
  # Exact bytes in, exact bytes out, whatever line-ending conversion this machine's Git does.
  git -C "$1" config core.autocrlf false
}
# python3 where it runs; on Windows that name is often only the Microsoft Store's installer stub.
python3() {
  if command python3 -c '' 2>/dev/null; then command python3 "$@"; else command python "$@"; fi
}
mkdir "$dir/alpha" "$dir/beta" "$dir/gamma"
mk "$dir/alpha"
mk "$dir/beta"
python3 - "$dir" <<'PY'
import pathlib  # files are written with LF, as on Linux (Python on Windows would write CRLF)
_write_text = pathlib.Path.write_text
pathlib.Path.write_text = lambda self, data, *a, **k: _write_text(self, data, *a, **{"newline": chr(10), **k})
import sys
from pathlib import Path
d = Path(sys.argv[1])
a = d / "alpha"
(a / "internal/greet").mkdir(parents=True)
(a / "internal/greet/greet.go").write_text('package greet\n\n// Hello says hello.\nfunc Hello(name string) string {\n\treturn "hello " + name\n}\n')
(a / "main.go").write_text('package main\n\nimport "example.test/alpha/internal/greet"\n\nfunc main() {\n\tprintln(greet.Hello("falcon"))\n}\n')
(a / "go.mod").write_text("module example.test/alpha\n\ngo 1.22\n")
(a / "notes.md").write_text("# Alpha\n\nA small repository for tests.\n")
(a / "big.py").write_text("".join(f"def function_{i}():\n    return {i}\n\n" for i in range(20000)))
b = d / "beta"
(b / "app.py").write_text("def beta_only():\n    return 'beta'\n")
PY
for r in alpha beta; do
  git -C "$dir/$r" add -A
  git -C "$dir/$r" -c commit.gpgSign=false commit -qm initial
done
# gamma: every file type, branches, a merge and a tag.
mkdir -p "$dir/gamma"
mk "$dir/gamma"
python3 - "$dir/gamma" "$project/web/node_modules/@fontsource/jetbrains-mono/files/jetbrains-mono-latin-400-normal.woff2" <<'PY'
import pathlib  # files are written with LF, as on Linux (Python on Windows would write CRLF)
_write_text = pathlib.Path.write_text
pathlib.Path.write_text = lambda self, data, *a, **k: _write_text(self, data, *a, **{"newline": chr(10), **k})
import sys, os, zlib, struct, math, wave, shutil
from pathlib import Path
g = Path(sys.argv[1])
(g / "assets").mkdir(); (g / "docs").mkdir()
def png(path, w, h):
    raw = b"".join(b"\x00" + bytes(sum(([x % 256, y % 256, 160] for x in range(w)), [])) for y in range(h))
    def chunk(t, d): return struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d) & 0xffffffff)
    path.write_bytes(b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b""))
png(g / "assets/pic.png", 64, 40)
(g / "assets/logo.svg").write_text('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10" width="100" height="100"><script>parent.svgRan=1</script><circle cx="5" cy="5" r="4" fill="#7856bf"/></svg>')
(g / "docs/report.pdf").write_bytes(b"%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 100]>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n")
(g / "data.csv").write_text('name,lang,stars\n"Falcon, viewer",Go,12\nother,TS,3\n')
w = wave.open(str(g / "assets/beep.wav"), "wb"); w.setnchannels(1); w.setsampwidth(2); w.setframerate(8000)
w.writeframes(b"".join(struct.pack("<h", int(6000 * math.sin(i / 3))) for i in range(4000))); w.close()
shutil.copy(sys.argv[2], g / "assets/mono.woff2")
(g / "assets/blob.bin").write_bytes(bytes(range(256)) * 4)
(g / "page.html").write_text("<script>alert(1)</script><h1>hi</h1>\n")
(g / "docs/guide.md").write_text("# Guide\n\n## Install\n\nRun make.\n")
(g / "README.md").write_text("""# Gamma

[![badge](https://ci.example/b.svg)](https://ci.example) <img src="assets/logo.svg" width="40" alt="logo">

Read the [guide](docs/guide.md) or jump to [Lists](#lists).

![picture](assets/pic.png)

```go
func main() {}
```

## Lists

- [x] done
- [ ] open
""")
(g / "list.txt").write_text("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n")
PY
git -C "$dir/gamma" add -A
git -C "$dir/gamma" -c commit.gpgSign=false commit -qm "Start gamma"
git -C "$dir/gamma" switch -qc side
echo "side" > "$dir/gamma/side.txt"
git -C "$dir/gamma" add side.txt
git -C "$dir/gamma" -c commit.gpgSign=false commit -qm "Side work"
git -C "$dir/gamma" switch -q main
git -C "$dir/gamma" -c commit.gpgSign=false merge -q --no-ff side -m "Merge side"
git -C "$dir/gamma" tag v1.0
git init -q --bare -b main "$dir/remote.git"
git -C "$dir/alpha" remote add origin "$dir/remote.git"
cd "$project"
exec go run ./cmd/falcon -data "$dir/data" -listen 127.0.0.1:7733 -lsp=false -assets "$project/web/dist" "$dir/alpha" "$dir/beta" "$dir/gamma"
