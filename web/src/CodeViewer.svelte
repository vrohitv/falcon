<script lang="ts">
  // The read-only code view: only the lines on screen are in the page (a file of any length opens and
  // scrolls at once), highlighted by the server a window at a time; selection, find, definitions,
  // hovers and problems are drawn over them.
  import { onMount } from "svelte";
  import { api, apiURL } from "./api";
  import { CHUNK, cachedWindow, codeWindow, type CodeWindow as Window } from "./filecache";
  import {
    definitions,
    hoverAt,
    lspStatus,
    lspStateFor,
    lspUsable,
    problemsFor,
    syntaxDefinitions,
    wordAt,
    type LspMethod,
    type Problem,
    type Target,
  } from "./definition";
  import { lsp } from "./lsp.svelte";
  import Markdown from "./Markdown.svelte";
  import LspStatus from "./LspStatus.svelte";
  import { fly } from "./motion";
  import {
    ArrowUpRight,
    Copy,
    FileCode2,
    RefreshCw,
    Search,
    ArrowUp,
    ArrowDown,
    X,
    CaseSensitive,
    WholeWord,
    Regex,
  } from "@lucide/svelte";
  let {
    path,
    startLine = 1,
    onposition,
    onjump,
    onnotice,
    disk = null,
    onreferences,
    startSelect = null,
    restore = null,
  }: {
    path: string;
    startLine?: number;
    onposition?: (line: number, character: number, hash: string) => void;
    /** Another file's definition was chosen. */
    onjump?: (path: string, line: number, column?: number, end?: number) => void;
    onnotice?: (message: string) => void;
    disk?: { path: string; hash: string; exists: boolean; n: number } | null;
    /** Find references to the name at the caret (Shift+F12, the context menu). */
    onreferences?: () => void;
    /** A match to select on startLine (a search result). */
    startSelect?: { col: number; len: number } | null;
    /** Where this file was left (its tab is shown again). */
    restore?: ViewState | null;
  } = $props();
  export type ViewState = { top: number; left: number; caret: { line: number; col: number }; anchor: { line: number; col: number } | null };
  let updated = $state(0);
  type Position = { line: number; col: number };
  let viewport: HTMLDivElement;
  let rows: HTMLDivElement;
  let meta = $state<Window | null>(null);
  let error = $state("");
  let busy = $state(false);
  let current = $state(1);
  let selectionLabel = $state("");
  let jump = $state("");
  let selected: { a: Position; f: Position } | null = null;
  const LH = 22,
    OVERSCAN = 24,
    MAX_CHUNKS = 8;
  const chunks = new Map<number, string[]>(),
    pending = new Set<number>();
  let generation = 0,
    raf = 0,
    controller = new AbortController(),
    destroyed = false;
  function position(node: Node | null, offset: number): Position | null {
    if (!node) return null;
    const element =
      node.nodeType === 1 ? (node as Element) : node.parentElement;
    const row = element?.closest<HTMLElement>("[data-line]");
    if (!row || !rows.contains(row)) return null;
    const code = row.querySelector(".code-text")!;
    const range = document.createRange();
    range.selectNodeContents(code);
    try {
      const cmp = range.comparePoint(node, offset);
      if (cmp < 0) return { line: Number(row.dataset.line), col: 0 };
      if (cmp > 0)
        return {
          line: Number(row.dataset.line),
          col: code.textContent?.length || 0,
        };
      range.setEnd(node, offset);
      return { line: Number(row.dataset.line), col: range.toString().length };
    } catch {
      return null;
    }
  }
  function point(p: Position): [Node, number] | null {
    const code = rows.querySelector(`[data-line="${p.line}"] .code-text`);
    if (!code) return null;
    let at = 0;
    const walker = document.createTreeWalker(code, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const length = n.nodeValue?.length || 0;
      if (p.col <= at + length) return [n, Math.max(0, p.col - at)];
      at += length;
    }
    return [code, code.childNodes.length];
  }
  // Selection is Falcon's own, as in VS Code's editor: an anchor and a head (the caret) in line and
  // column terms, drawn with the CSS Highlight API over whichever lines are on screen. The browser's own
  // selection cannot work in a virtual list: it lives on DOM nodes that scrolling replaces, and it cannot
  // reach lines that are not rendered (so a drag that scrolled lost its start, and a selection scrolled
  // away could not be copied).
  let anchorPos: Position | null = null;
  let column = $state(1);
  let caretPos: Position = { line: 1, col: 0 };
  let goalCol: number | null = null;
  let caretEl: HTMLDivElement;
  let sizer: HTMLDivElement;
  const same = (a: Position, b: Position) => a.line === b.line && a.col === b.col;
  const before = (a: Position, b: Position) => a.line < b.line || (a.line === b.line && a.col < b.col);
  function lineText(line: number): string | null {
    const row = rows?.querySelector<HTMLElement>(`[data-line="${line}"]`);
    if (!row || row.dataset.pending) return null;
    return row.querySelector(".code-text")?.textContent || "";
  }
  function clampPos(p: Position): Position {
    const line = Math.max(1, Math.min(p.line, meta?.total || 1));
    const text = lineText(line);
    return { line, col: Math.max(0, text === null ? p.col : Math.min(p.col, text.length)) };
  }
  function capture() {
    if (selected) {
      const a = before(selected.a, selected.f) ? selected.a : selected.f,
        f = a === selected.a ? selected.f : selected.a;
      const lines = f.line - a.line + (f.col > 0 || f.line === a.line ? 1 : 0);
      selectionLabel = lines > 1 ? `${lines.toLocaleString()} lines selected` : `${f.col - a.col} selected`;
    } else selectionLabel = "";
    column = caretPos.col + 1;
  }
  /** Moves the caret, extending from the anchor (a selection) or not. */
  function setCaret(head: Position, anchor: Position | null, reveal = true) {
    caretPos = clampPos(head);
    anchorPos = anchor && !same(anchor, caretPos) ? anchor : null;
    selected = anchorPos ? { a: anchorPos, f: caretPos } : null;
    const lineChanged = current !== caretPos.line;
    current = caretPos.line;
    caretCol = caretPos.col;
    capture();
    onposition?.(current - 1, caretCol, meta?.hash || "");
    if (reveal) revealCaret();
    if (lineChanged) schedule();
    else drawSelection();
  }
  function revealCaret() {
    if (!viewport) return;
    const top = (caretPos.line - 1) * LH;
    if (top < viewport.scrollTop) viewport.scrollTop = top;
    else if (top + LH > viewport.scrollTop + viewport.clientHeight) viewport.scrollTop = top + LH - viewport.clientHeight;
  }
  /** Where the reader is: scroll, caret and selection (kept per tab). */
  export function viewState(): ViewState {
    return { top: viewport?.scrollTop || 0, left: viewport?.scrollLeft || 0, caret: caretPos, anchor: anchorPos };
  }
  /** Goes to a line of this file without reloading it, selecting a match there if given. */
  export function revealAt(line: number, sel: { col: number; len: number } | null = null) {
    if (!meta) return;
    if (sel) selectRange({ line, col: sel.col }, { line, col: sel.col + sel.len });
    else go(line);
  }
  /** Selects a range (find, references): the caret at its end, scrolled into view. */
  export function selectRange(from: Position, to: Position) {
    if (!meta) return;
    if ((from.line - 1) * LH < viewport.scrollTop || from.line * LH > viewport.scrollTop + viewport.clientHeight)
      viewport.scrollTop = Math.max(0, (from.line - 1) * LH - viewport.clientHeight / 3);
    setCaret(to, from, false);
    schedule();
  }
  /** Draws the selection and the caret over the rendered lines. */
  function drawSelection() {
    if (!rows || !sizer) return;
    // Positions given as "the end of the line" before the line was on screen become real columns.
    const text = lineText(caretPos.line);
    if (text !== null && caretPos.col > text.length) {
      caretPos = { line: caretPos.line, col: text.length };
      if (selected) selected = { a: selected.a, f: caretPos };
      caretCol = caretPos.col;
      capture();
    }
    const registry = supportsHighlight ? (CSS as any).highlights : null;
    const ranges: Range[] = [];
    let a: Position | null = null,
      f: Position | null = null;
    if (selected) {
      a = before(selected.a, selected.f) ? selected.a : selected.f;
      f = a === selected.a ? selected.f : selected.a;
    }
    for (const row of rows.children as HTMLCollectionOf<HTMLElement>) {
      const line = Number(row.dataset.line);
      const inside = !!a && !!f && line >= a.line && line <= f.line;
      // The line break of a fully crossed line shows as a small block after its text.
      row.classList.toggle("sel-eol", inside && line < f!.line);
      row.classList.toggle("sel-fallback", inside && !registry);
      if (!inside || !registry) continue;
      const text = row.querySelector(".code-text")!.textContent || "";
      const from = line === a!.line ? Math.min(a!.col, text.length) : 0,
        to = line === f!.line ? Math.min(f!.col, text.length) : text.length;
      if (to <= from) continue;
      const s0 = point({ line, col: from }),
        s1 = point({ line, col: to });
      if (!s0 || !s1) continue;
      const range = document.createRange();
      try {
        range.setStart(s0[0], s0[1]);
        range.setEnd(s1[0], s1[1]);
        ranges.push(range);
      } catch {}
    }
    if (registry) {
      if (ranges.length) registry.set("code-selection", new (window as any).Highlight(...ranges));
      else registry.delete("code-selection");
    }
    // The caret: a thin bar where the head is, when its line is on screen.
    const at = point(caretPos);
    if (!caretEl) return;
    if (!at) {
      caretEl.style.display = "none";
      return;
    }
    const range = document.createRange();
    range.setStart(at[0], at[1]);
    range.collapse(true);
    let box = range.getBoundingClientRect();
    if (!box.height) {
      const code = rows.querySelector(`[data-line="${caretPos.line}"] .code-text`);
      box = code ? code.getBoundingClientRect() : box;
    }
    const base = sizer.getBoundingClientRect();
    caretEl.style.display = "block";
    caretEl.style.transform = `translate(${box.left - base.left}px, ${(caretPos.line - 1) * LH}px)`;
    caretEl.classList.remove("blink");
    void caretEl.offsetWidth;
    caretEl.classList.add("blink");
  }

  // Find in the file (Ctrl+F): over the whole file, not just the lines on screen. The text is fetched once
  // per version of the file; matches are highlighted where they are visible, the current one selected.
  type Match = { line: number; col: number; len: number };
  let findOpen = $state(false),
    findQuery = $state(""),
    findCase = $state(false),
    findWord = $state(false),
    findRegex = $state(false),
    findError = $state(""),
    matches = $state<Match[]>([]),
    findIndex = $state(-1),
    findInput = $state<HTMLInputElement>();
  let fullText: { hash: string; lines: string[] } | null = null;
  let findTimer: ReturnType<typeof setTimeout> | undefined;
  async function wholeText(): Promise<string[]> {
    if (fullText && fullText.hash === meta?.hash) return fullText.lines;
    const res = await fetch(apiURL("/api/raw?path=" + encodeURIComponent(path)), { signal: controller.signal });
    if (!res.ok) throw new Error("The file could not be read for searching");
    const lines = (await res.text()).replace(/\r\n?/g, "\n").split("\n");
    fullText = { hash: meta?.hash || "", lines };
    return lines;
  }
  function pattern(): RegExp | null {
    if (!findQuery) return null;
    let source = findRegex ? findQuery : findQuery.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    if (findWord) source = `(?<![\\p{L}\\p{N}_$])(?:${source})(?![\\p{L}\\p{N}_$])`;
    try {
      findError = "";
      return new RegExp(source, "gu" + (findCase ? "" : "i"));
    } catch (e) {
      findError = "Invalid regular expression";
      return null;
    }
  }
  async function runFind(keepIndex = false) {
    const re = pattern();
    if (!re) {
      matches = [];
      findIndex = -1;
      drawFind();
      return;
    }
    let lines: string[];
    try {
      lines = await wholeText();
    } catch (e) {
      findError = (e as Error).message;
      return;
    }
    const found: Match[] = [];
    outer: for (let i = 0; i < lines.length; i++) {
      re.lastIndex = 0;
      for (let m = re.exec(lines[i]); m; m = re.exec(lines[i])) {
        if (m[0].length === 0) {
          re.lastIndex++;
          continue;
        }
        found.push({ line: i + 1, col: m.index, len: m[0].length });
        if (found.length >= 20000) break outer;
      }
    }
    matches = found;
    if (!found.length) findIndex = -1;
    else if (!keepIndex || findIndex < 0 || findIndex >= found.length) {
      // The first match at or after the caret.
      const from = anchorPos && before(anchorPos, caretPos) ? anchorPos : caretPos;
      const i = found.findIndex((m) => m.line > from.line || (m.line === from.line && m.col >= from.col));
      findIndex = i < 0 ? 0 : i;
      revealMatch();
    }
    drawFind();
  }
  function revealMatch() {
    const m = matches[findIndex];
    if (!m) return;
    selectRange({ line: m.line, col: m.col }, { line: m.line, col: m.col + m.len });
    // Long lines: bring the match into view sideways too.
    requestAnimationFrame(() => {
      const at = point({ line: m.line, col: m.col });
      if (!at) return;
      const r = document.createRange();
      r.setStart(at[0], at[1]);
      const box = r.getBoundingClientRect(),
        view = viewport.getBoundingClientRect();
      if (box.left < view.left + 70 || box.left > view.right - 40) viewport.scrollLeft += box.left - view.left - view.width / 2;
    });
  }
  function step(dir: 1 | -1) {
    if (!matches.length) return;
    findIndex = (findIndex + dir + matches.length) % matches.length;
    revealMatch();
    drawFind();
  }
  function drawFind() {
    if (!supportsHighlight || !rows) return;
    const registry = (CSS as any).highlights;
    if (!findOpen || !matches.length) {
      registry.delete("find-match");
      registry.delete("find-current");
      return;
    }
    const first = Number((rows.firstElementChild as HTMLElement | null)?.dataset.line || 0),
      last = Number((rows.lastElementChild as HTMLElement | null)?.dataset.line || 0);
    let lo = 0,
      hi = matches.length;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (matches[mid].line < first) lo = mid + 1;
      else hi = mid;
    }
    const all: Range[] = [],
      now: Range[] = [];
    for (let i = lo; i < matches.length && matches[i].line <= last; i++) {
      const m = matches[i];
      const a = point({ line: m.line, col: m.col }),
        b = point({ line: m.line, col: m.col + m.len });
      if (!a || !b) continue;
      const r = document.createRange();
      try {
        r.setStart(a[0], a[1]);
        r.setEnd(b[0], b[1]);
      } catch {
        continue;
      }
      (i === findIndex ? now : all).push(r);
    }
    registry.set("find-match", new (window as any).Highlight(...all));
    registry.set("find-current", new (window as any).Highlight(...now));
  }
  /** Opens find, seeded with the selection (one line) or the name at the caret. */
  export function find() {
    if (!meta) return false;
    let seed = "";
    if (selected && selected.a.line === selected.f.line) {
      const text = lineText(selected.a.line) || "";
      seed = text.slice(Math.min(selected.a.col, selected.f.col), Math.max(selected.a.col, selected.f.col));
    } else if (!findQuery) seed = caretWord()?.word || "";
    if (seed) findQuery = seed;
    findOpen = true;
    requestAnimationFrame(() => {
      findInput?.focus();
      findInput?.select();
    });
    void runFind();
    return true;
  }
  function closeFind() {
    findOpen = false;
    drawFind();
    viewport.focus({ preventScroll: true });
  }
  function findKey(e: KeyboardEvent) {
    if (e.key === "Enter") {
      e.preventDefault();
      step(e.shiftKey ? -1 : 1);
    } else if (e.key === "Escape") {
      e.preventDefault();
      closeFind();
    } else if (e.altKey && ["c", "w", "r"].includes(e.code.slice(-1).toLowerCase())) {
      e.preventDefault();
      const k = e.code.slice(-1).toLowerCase();
      if (k === "c") findCase = !findCase;
      if (k === "w") findWord = !findWord;
      if (k === "r") findRegex = !findRegex;
      void runFind();
    } else if (e.ctrlKey && e.key.toLowerCase() === "f") {
      e.preventDefault();
      findInput?.select();
    }
  }

  // The mouse: click places the caret, drag selects (scrolling when it leaves the view), double-click
  // selects a word, triple-click a line; Shift-click extends; the gutter selects whole lines.
  let dragging: { mode: "char" | "word" | "line"; origin: Position; originEnd: Position } | null = null;
  let lastPointer = { x: 0, y: 0 },
    autoscroll = 0;
  function posFromPoint(x: number, y: number): Position {
    const base = sizer.getBoundingClientRect();
    const line = Math.max(1, Math.min(meta?.total || 1, Math.floor((y - base.top) / LH) + 1));
    const row = rows.querySelector<HTMLElement>(`[data-line="${line}"]`);
    const code = row?.querySelector<HTMLElement>(".code-text");
    if (!code) return { line, col: y < base.top ? 0 : 0 };
    const box = code.getBoundingClientRect();
    const text = code.textContent || "";
    if (x <= box.left) return { line, col: 0 };
    const doc = document as any;
    const midY = base.top + (line - 1) * LH + LH / 2;
    let node: Node | null = null,
      offset = 0;
    if (doc.caretPositionFromPoint) {
      const p = doc.caretPositionFromPoint(Math.min(x, box.right - 1), midY);
      node = p?.offsetNode ?? null;
      offset = p?.offset ?? 0;
    } else if (doc.caretRangeFromPoint) {
      const r = doc.caretRangeFromPoint(Math.min(x, box.right - 1), midY);
      node = r?.startContainer ?? null;
      offset = r?.startOffset ?? 0;
    }
    const p = node && code.contains(node) ? position(node, offset) : null;
    if (!p) return { line, col: x > box.left ? text.length : 0 };
    // Past the end of the text: the end of the line.
    const end = point({ line, col: text.length });
    if (end) {
      const r = document.createRange();
      r.setStart(end[0], end[1]);
      const endBox = r.getBoundingClientRect();
      if (endBox.width === 0 && endBox.left && x > endBox.left) return { line, col: text.length };
    }
    return p;
  }
  function wordRange(p: Position): [Position, Position] {
    const text = lineText(p.line) || "";
    const isWord = (c: string) => /[\p{L}\p{N}_$]/u.test(c);
    const isSpace = (c: string) => c === " " || c === "\t";
    let start = p.col,
      end = p.col;
    const at = text[p.col] ?? text[p.col - 1] ?? "";
    const kind = isWord(at) ? isWord : isSpace(at) ? isSpace : (c: string) => !isWord(c) && !isSpace(c) && c !== undefined;
    while (start > 0 && kind(text[start - 1])) start--;
    while (end < text.length && kind(text[end])) end++;
    if (end === start && end < text.length) end++;
    return [{ line: p.line, col: start }, { line: p.line, col: end }];
  }
  // Word jumps (Alt+← Alt+→, Ctrl elsewhere), as in VS Code: spaces are skipped, then a word or a run of
  // punctuation is crossed.
  function wordEdge(p: Position, dir: 1 | -1): Position {
    const text = lineText(p.line) || "";
    const isWord = (c: string) => /[\p{L}\p{N}_$]/u.test(c);
    const isSpace = (c: string) => c === " " || c === "\t";
    let i = p.col;
    if (dir > 0) {
      while (i < text.length && isSpace(text[i])) i++;
      const word = isWord(text[i] ?? "");
      while (i < text.length && !isSpace(text[i]) && isWord(text[i]) === word) i++;
    } else {
      while (i > 0 && isSpace(text[i - 1])) i--;
      const word = isWord(text[i - 1] ?? "");
      while (i > 0 && !isSpace(text[i - 1]) && isWord(text[i - 1]) === word) i--;
    }
    return { line: p.line, col: i };
  }
  // Word jumps: Ctrl+← and Ctrl+→ (Alt+← and Alt+→ go Back and Forward).
  const wordKey = (e: KeyboardEvent) => e.ctrlKey;
  function lineRange(line: number): [Position, Position] {
    const total = meta?.total || 1;
    return line < total ? [{ line, col: 0 }, { line: line + 1, col: 0 }] : [{ line, col: 0 }, { line, col: (lineText(line) || "").length }];
  }
  function extendTo(p: Position) {
    if (!dragging) return;
    const { mode, origin, originEnd } = dragging;
    if (mode === "char") return setCaret(p, origin, true);
    const [s, e] = mode === "word" ? wordRange(p) : lineRange(p.line);
    if (before(p, origin)) setCaret(s, originEnd, true);
    else setCaret(e, origin, true);
  }
  function pointerDown(event: MouseEvent) {
    if (event.button !== 0) return;
    const target = event.target as Element;
    if (target.closest(".code-menu, .lsp-hover, .def-picker, .def-peek")) return;
    // Ctrl+click goes to a definition (the click handler); the browser's own selection never starts.
    event.preventDefault();
    viewport.focus({ preventScroll: true });
    window.getSelection()?.removeAllRanges();
    if (event.ctrlKey) {
      // Ctrl+click goes to the definition when the button comes up where it went down (as in VS Code).
      const at = { x: event.clientX, y: event.clientY };
      const up = (e: MouseEvent) => {
        window.removeEventListener("mouseup", up);
        if (e.button === 0 && Math.abs(e.clientX - at.x) < 4 && Math.abs(e.clientY - at.y) < 4) definitionAt(e);
      };
      window.addEventListener("mouseup", up);
      return;
    }
    hoverCard = null;
    menu = null;
    picker = null;
    goalCol = null;
    const p = posFromPoint(event.clientX, event.clientY);
    const inGutter = !!target.closest(".code-gutter");
    // Click counts cycle (a fourth quick click starts again at one); Shift-click always extends.
    const clicks = inGutter ? 3 : (((event.detail || 1) - 1) % 3) + 1;
    if (event.shiftKey) {
      dragging = { mode: "char", origin: anchorPos ?? caretPos, originEnd: anchorPos ?? caretPos };
      setCaret(p, dragging.origin);
    } else if (clicks === 2) {
      const [s, e] = wordRange(p);
      dragging = { mode: "word", origin: s, originEnd: e };
      setCaret(e, s);
    } else if (clicks === 3) {
      const [s, e] = lineRange(p.line);
      dragging = { mode: "line", origin: s, originEnd: e };
      setCaret(e, s, false);
    } else {
      dragging = { mode: "char", origin: p, originEnd: p };
      setCaret(p, null);
    }
    lastPointer = { x: event.clientX, y: event.clientY };
    const move = (e: MouseEvent) => {
      lastPointer = { x: e.clientX, y: e.clientY };
      extendTo(posFromPoint(e.clientX, e.clientY));
      const box = viewport.getBoundingClientRect();
      if ((e.clientY < box.top || e.clientY > box.bottom || e.clientX < box.left || e.clientX > box.right) && !autoscroll)
        autoscroll = requestAnimationFrame(scrollWhileDragging);
    };
    const up = () => {
      dragging = null;
      cancelAnimationFrame(autoscroll);
      autoscroll = 0;
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  }
  // Dragging past an edge scrolls, faster the further out the pointer is.
  function scrollWhileDragging() {
    autoscroll = 0;
    if (!dragging) return;
    const box = viewport.getBoundingClientRect();
    const { x, y } = lastPointer;
    const dy = y < box.top ? y - box.top : y > box.bottom ? y - box.bottom : 0;
    const dx = x < box.left ? x - box.left : x > box.right ? x - box.right : 0;
    if (!dy && !dx) return;
    viewport.scrollTop += Math.sign(dy) * Math.min(80, 4 + Math.abs(dy) / 2);
    viewport.scrollLeft += Math.sign(dx) * Math.min(60, 4 + Math.abs(dx) / 2);
    paint();
    extendTo(posFromPoint(Math.max(box.left + 60, Math.min(x, box.right - 2)), Math.max(box.top + 1, Math.min(y, box.bottom - 1))));
    autoscroll = requestAnimationFrame(scrollWhileDragging);
  }
  function schedule() {
    if (!raf)
      raf = requestAnimationFrame(() => {
        raf = 0;
        paint();
      });
  }
  // The lines on screen (and a margin either side) are the only ones in the page. Rows already there are
  // kept and brought up to date in place, so scrolling only adds and removes rows at the edges, and a
  // row's text is only rewritten when it changed (a window arriving, or refined).
  const rowCache = new Map<number, { row: HTMLElement; gutter: HTMLElement; code: HTMLElement; html: string | null }>();
  function rowFor(line: number) {
    let r = rowCache.get(line);
    if (!r) {
      const row = document.createElement("div");
      row.dataset.line = String(line);
      const gutter = document.createElement("span");
      gutter.className = "code-gutter";
      gutter.textContent = String(line);
      const code = document.createElement("span");
      code.className = "code-text";
      row.append(gutter, code);
      r = { row, gutter, code, html: null };
      rowCache.set(line, r);
    }
    return r;
  }
  function paint() {
    if (!meta || !viewport || !rows) return;
    const shown = Math.ceil(viewport.clientHeight / LH);
    const top = Math.floor(viewport.scrollTop / LH);
    const from = Math.max(0, top - OVERSCAN),
      to = Math.min(meta.total, top + shown + OVERSCAN);
    for (let w = Math.floor(from / CHUNK); w * CHUNK < Math.max(to, from + 1); w++) void load(w);
    for (const line of rowCache.keys()) if (line <= from || line > to) rowCache.delete(line);
    const list: HTMLElement[] = [];
    for (let line = from + 1; line <= to; line++) {
      const r = rowFor(line);
      const lines = chunks.get(Math.floor((line - 1) / CHUNK));
      const html = lines ? (lines[(line - 1) % CHUNK] ?? "") : null;
      if (html !== r.html) {
        r.code.innerHTML = html ?? "";
        r.html = html;
      }
      // Not loaded yet: its text is unknown, not empty (selection must not clamp to it).
      if (html === null) r.row.dataset.pending = "1";
      else delete r.row.dataset.pending;
      const marks = byLine.get(line);
      r.row.className = "code-row" + (line === current ? " current" : "") + (marks ? " has-" + marks[0].severity : "");
      r.gutter.title = marks ? marks.map((d) => `${d.severity}: ${d.message}`).join("\n") : "";
      list.push(r.row);
    }
    rows.style.transform = `translateY(${from * LH}px)`;
    rows.replaceChildren(...list);
    squiggle(from + 1, to);
    drawSelection();
    drawFind();
  }
  async function load(chunk: number, force = false) {
    if (destroyed || pending.has(chunk) || (!force && chunks.has(chunk)))
      return;
    // A file seen before opens from the browser cache at its last known
    // hash; the open tab's watcher event (or a stat) confirms that hash and
    // refreshes in place if it moved.
    if (!meta && !force) {
      const hit = cachedWindow(path, chunk * CHUNK);
      if (hit) {
        meta = hit;
        chunks.set(chunk, hit.lines);
        schedule();
        if (hit.refine) {
          const gen = generation;
          setTimeout(() => {
            if (!destroyed && gen === generation) void load(chunk, true);
          }, 600);
        }
        return;
      }
    }
    pending.add(chunk);
    const gen = generation;
    try {
      const data = await codeWindow(path, chunk * CHUNK, meta?.hash || "", controller.signal, force);
      if (destroyed || gen !== generation) return;
      if (!meta) meta = data;
      chunks.delete(chunk);
      chunks.set(chunk, data.lines);
      while (chunks.size > MAX_CHUNKS)
        chunks.delete(chunks.keys().next().value!);
      error = "";
      schedule();
      if (data.refine && !force)
        setTimeout(() => {
          if (!destroyed && gen === generation) void load(chunk, true);
        }, 1200);
    } catch (err) {
      if (controller.signal.aborted) return;
      if (String((err as Error).message).includes("source changed")) {
        if (gen === generation) pending.delete(chunk);
        void refresh();
        return;
      }
      error = String((err as Error).message);
    } finally {
      if (gen === generation) pending.delete(chunk);
    }
  }
  let first = true;
  async function reload() {
    generation++;
    problems = [];
    hoverCard = null;
    controller.abort();
    controller = new AbortController();
    chunks.clear();
    pending.clear();
    rowCache.clear();
    meta = null;
    selected = null;
    anchorPos = null;
    caretPos = { line: 1, col: 0 };
    selectionLabel = "";
    await load(0);
    if (first && restore) {
      // Back to a tab: the same place, caret and selection.
      viewport.scrollTop = restore.top;
      viewport.scrollLeft = restore.left;
      paint();
      setCaret(restore.caret, restore.anchor, false);
    } else if (first && startSelect) {
      go(startLine);
      selectRange({ line: startLine, col: startSelect.col }, { line: startLine, col: startSelect.col + startSelect.len });
    } else go(startLine);
    first = false;
    void checkProblems();
    if (findOpen) void runFind(true);
  }
  // The file changed on disk (an integration, a branch switch, a terminal or
  // another editor): reload in place, keeping the reader's position.
  async function refresh() {
    const top = viewport.scrollTop;
    await reload();
    viewport.scrollTop = top;
    schedule();
    updated++;
  }
  $effect(() => {
    if (disk && disk.path === path && disk.exists && meta && disk.hash && disk.hash !== meta.hash)
      void refresh();
  });
  function go(line: number) {
    if (!meta) return;
    current = Math.max(1, Math.min(line, meta.total));
    caretPos = { line: current, col: 0 };
    anchorPos = null;
    selected = null;
    capture();
    viewport.scrollTop = Math.max(0, (current - 4) * LH);
    onposition?.(current - 1, 0, meta.hash);
    schedule();
  }
  async function rawSelection() {
    const a = selected?.a ?? { line: current, col: 0 },
      f = selected?.f ?? { line: current, col: Infinity };
    const start =
        a.line < f.line || (a.line === f.line && a.col <= f.col) ? a : f,
      end = start === a ? f : a;
    // A long selection (select all, say) is copied from the whole file, read once.
    if (end.line - start.line > 1500) {
      const lines = await wholeText();
      const part = lines.slice(start.line - 1, end.line);
      if (part.length === 1) return part[0].slice(start.col, end.col);
      part[0] = part[0].slice(start.col);
      part[part.length - 1] = part[part.length - 1].slice(0, end.col);
      return part.join("\n");
    }
    const text: string[] = [];
    for (let line = start.line; line <= end.line; line += 1000) {
      const data = await api<Window>(
        `/api/code?path=${encodeURIComponent(path)}&start=${line - 1}&count=${Math.min(1000, end.line - line + 1)}&raw=true&hash=${meta?.hash}`,
      );
      text.push(...data.raw);
    }
    if (text.length === 1) return text[0].slice(start.col, end.col);
    text[0] = text[0].slice(start.col);
    text[text.length - 1] = text[text.length - 1].slice(0, end.col);
    return text.join("\n");
  }
  async function copy() {
    try {
      await navigator.clipboard.writeText(await rawSelection());
      selectionLabel = "Copied";
    } catch (err) {
      error = (err as Error).message;
    }
  }
  /** Copy where this is: path:line (or path:first-last for a selection), for a chat, an issue or a terminal. */
  async function anchor() {
    const first = Math.min(selected?.a.line ?? current, selected?.f.line ?? current),
      last = Math.max(selected?.a.line ?? current, selected?.f.line ?? current);
    try {
      await navigator.clipboard.writeText(`${path}:${first}${last > first ? "-" + last : ""}`);
      selectionLabel = "Reference copied";
    } catch (err) {
      error = (err as Error).message;
    }
  }
  function focusFirst(node: HTMLElement, first: boolean) {
    if (first) node.focus();
  }
  function definitionAt(event: MouseEvent) {
    const w = wordFromPoint(event.clientX, event.clientY);
    if (w) {
      event.preventDefault();
      setCaret({ line: w.line, col: w.start }, null, false);
      void goToDefinition(w, { x: event.clientX, y: event.clientY });
    }
  }
  function key(event: KeyboardEvent) {
    if (event.key === "F12") {
      event.preventDefault();
      if (event.shiftKey) onreferences?.();
      else if (event.ctrlKey) lookAtCaret("textDocument/implementation");
      else definitionAtCaret();
      return;
    }
    if (event.key === "F8") {
      event.preventDefault();
      nextProblem(event.shiftKey ? -1 : 1);
      return;
    }
    if (event.key === "Escape" && (menu || problemList || hoverCard)) {
      menu = null;
      problemList = false;
      hoverCard = null;
      return;
    }
    if (event.key === "Escape" && picker) {
      picker = null;
      return;
    }
    const mod = event.ctrlKey;
    if (event.key === "F3" || (mod && event.key.toLowerCase() === "g")) {
      event.preventDefault();
      if (!matches.length) find();
      else step(event.shiftKey ? -1 : 1);
      return;
    }
    if (event.key === "Escape" && findOpen) {
      closeFind();
      return;
    }
    if (event.key === "Escape" && selected) {
      setCaret(caretPos, null, false);
      return;
    }
    if (mod && event.key.toLowerCase() === "a") {
      event.preventDefault();
      const total = meta?.total || 1;
      setCaret({ line: total, col: 1e9 }, { line: 1, col: 0 }, false);
      return;
    }
    if (mod && event.key.toLowerCase() === "c") {
      event.preventDefault();
      void copy();
      return;
    }
    // The caret moves as in an editor: arrows, word jumps (Alt / Ctrl), line ends, pages, file ends;
    // with Shift each one selects.
    const page = Math.max(1, Math.floor(viewport.clientHeight / LH) - 1);
    const text = lineText(caretPos.line) || "";
    let next: Position | null = null;
    const vertical = (lines: number) => {
      goalCol ??= caretPos.col;
      return { line: caretPos.line + lines, col: goalCol };
    };
    switch (event.key) {
      case "ArrowDown":
        next = mod ? { line: meta?.total || 1, col: 1e9 } : vertical(1);
        break;
      case "ArrowUp":
        next = mod ? { line: 1, col: 0 } : vertical(-1);
        break;
      case "PageDown":
        next = vertical(page);
        viewport.scrollTop += page * LH;
        break;
      case "PageUp":
        next = vertical(-page);
        viewport.scrollTop -= page * LH;
        break;
      case "ArrowLeft":
        if (selected && !event.shiftKey) next = before(selected.a, selected.f) ? selected.a : selected.f;
        else if (wordKey(event)) next = caretPos.col === 0 ? { line: caretPos.line - 1, col: 1e9 } : wordEdge(caretPos, -1);
        else next = caretPos.col === 0 ? { line: caretPos.line - 1, col: 1e9 } : { line: caretPos.line, col: caretPos.col - 1 };
        break;
      case "ArrowRight":
        if (selected && !event.shiftKey) next = before(selected.a, selected.f) ? selected.f : selected.a;
        else if (wordKey(event)) next = caretPos.col >= text.length ? { line: caretPos.line + 1, col: 0 } : wordEdge(caretPos, 1);
        else next = caretPos.col >= text.length ? { line: caretPos.line + 1, col: 0 } : { line: caretPos.line, col: caretPos.col + 1 };
        break;
      case "Home":
        next = mod ? { line: 1, col: 0 } : { line: caretPos.line, col: caretPos.col === (text.match(/^\s*/)?.[0].length ?? 0) ? 0 : text.match(/^\s*/)?.[0].length ?? 0 };
        break;
      case "End":
        next = mod ? { line: meta?.total || 1, col: 1e9 } : { line: caretPos.line, col: text.length };
        break;
    }
    if (next) {
      event.preventDefault();
      if (!["ArrowDown", "ArrowUp", "PageDown", "PageUp"].includes(event.key) || mod) goalCol = null;
      const line = Math.max(1, Math.min(meta?.total || 1, next.line));
      const keep = goalCol;
      // A line not yet on screen is scrolled to first, so its text is known.
      if ((line - 1) * LH < viewport.scrollTop || line * LH > viewport.scrollTop + viewport.clientHeight) {
        caretPos = { line, col: next.col };
        revealCaret();
        paint();
      }
      setCaret({ line, col: next.col }, event.shiftKey ? (anchorPos ?? caretPos) : null);
      goalCol = keep;
      return;
    }
    if (event.altKey && event.key.toLowerCase() === "e") {
      event.preventDefault();
      void anchor();
    }
  }
  // Go to definition: hold Ctrl (Ctrl elsewhere) to underline the name under
  // the pointer and preview where it is defined; click, or press F12 on the
  // caret's name, to go there. Several answers open a picker.
  let linking = $state(false),
    peek = $state<{ x: number; y: number; name: string; targets: Target[] } | null>(null),
    picker = $state<{ x: number; y: number; name: string; targets: Target[]; kind?: string } | null>(null),
    finding = $state(false);
  let caretCol = 0,
    hovered: { line: number; word: string; start: number; end: number; qualifier: string } | null = null,
    peekTimer: ReturnType<typeof setTimeout> | undefined;
  const supportsHighlight = typeof CSS !== "undefined" && "highlights" in CSS;
  function wordFromPoint(x: number, y: number) {
    const doc = document as any;
    let node: Node | null = null,
      offset = 0;
    if (doc.caretPositionFromPoint) {
      const p = doc.caretPositionFromPoint(x, y);
      node = p?.offsetNode ?? null;
      offset = p?.offset ?? 0;
    } else if (doc.caretRangeFromPoint) {
      const r = doc.caretRangeFromPoint(x, y);
      node = r?.startContainer ?? null;
      offset = r?.startOffset ?? 0;
    }
    if (!node || !(node.parentElement?.closest(".code-text"))) return null;
    const p = position(node, offset);
    if (!p) return null;
    const text = rows.querySelector(`[data-line="${p.line}"] .code-text`)?.textContent || "";
    const w = wordAt(text, p.col);
    return w ? { line: p.line, ...w } : null;
  }
  function underline(w: typeof hovered) {
    if (!supportsHighlight) return;
    const registry = (CSS as any).highlights;
    if (!w) return registry.delete("definition-link");
    const a = point({ line: w.line, col: w.start }),
      b = point({ line: w.line, col: w.end });
    if (!a || !b) return;
    const range = document.createRange();
    range.setStart(a[0], a[1]);
    range.setEnd(b[0], b[1]);
    registry.set("definition-link", new (window as any).Highlight(range));
  }
  function unhover() {
    hovered = null;
    underline(null);
    clearTimeout(peekTimer);
    peek = null;
  }
  function hover(event: MouseEvent) {
    linking = event.ctrlKey;
    if (!linking) {
      unhover();
      return info(event);
    }
    clearTimeout(infoTimer);
    hoverCard = null;
    const w = wordFromPoint(event.clientX, event.clientY);
    if (w?.word === hovered?.word && w?.line === hovered?.line) return;
    unhover();
    if (!w) return;
    hovered = w;
    underline(w);
    const at = { x: event.clientX, y: event.clientY };
    peekTimer = setTimeout(async () => {
      const targets = await syntaxDefinitions(w.word, path, w.qualifier, w.line);
      if (hovered === w && targets.length) peek = { ...at, name: w.word, targets };
    }, 280);
  }
  const what: Record<LspMethod, string> = {
    "textDocument/definition": "definition",
    "textDocument/typeDefinition": "type definition",
    "textDocument/implementation": "implementation",
    "textDocument/references": "reference",
  };
  async function goToDefinition(
    w: { line: number; word: string; start: number; qualifier: string },
    at: { x: number; y: number },
    method: LspMethod = "textDocument/definition",
  ) {
    if (!meta || finding) return;
    finding = true;
    unhover();
    hoverCard = null;
    try {
      const status = await lspStatus();
      const { targets } = await definitions({
        path,
        name: w.word,
        qualifier: w.qualifier,
        line: w.line - 1,
        character: w.start,
        hash: meta.hash,
        semantic: lspUsable(lspStateFor(path, status).state),
        method,
      });
      const kind = what[method];
      const elsewhere = targets.filter((t) => !(t.path === path && t.line === w.line));
      if (!targets.length)
        onnotice?.(
          method === "textDocument/definition"
            ? `No definition found for ${w.word}`
            : `No ${kind} found for ${w.word}` + (lspUsable(lspStateFor(path, status).state) ? "" : " (needs the language server)"),
        );
      else if (!elsewhere.length) onnotice?.(`${w.word}'s ${kind} is here`);
      else if (elsewhere.length === 1) jumpTo(elsewhere[0]);
      else picker = { ...at, name: w.word, targets: elsewhere, kind };
    } finally {
      finding = false;
    }
  }
  /** Go to the type definition or implementations of the name at the caret. */
  function lookAtCaret(method: LspMethod) {
    const w = caretWord();
    if (!w) return onnotice?.("Place the caret on a name first");
    const box = rows.querySelector(`[data-line="${current}"]`)?.getBoundingClientRect();
    void goToDefinition(w, { x: (box?.left ?? 0) + 80, y: box?.bottom ?? 0 }, method);
  }

  // Problems: what the language server reports for this file, marked in the gutter and under the text,
  // listed from the status bar, stepped through with F8.
  let problems = $state<Problem[]>([]),
    problemList = $state(false),
    checking = $state(false);
  const byLine = new Map<number, Problem[]>();
  const rank = { error: 0, warning: 1, information: 2, hint: 3 } as const;
  $effect(() => {
    byLine.clear();
    for (const d of [...problems].sort((a, b) => rank[a.severity] - rank[b.severity]))
      byLine.set(d.line, [...(byLine.get(d.line) || []), d]);
    schedule();
  });
  const counts = $derived({
    error: problems.filter((d) => d.severity === "error").length,
    warning: problems.filter((d) => d.severity === "warning").length,
    other: problems.filter((d) => d.severity === "information" || d.severity === "hint").length,
  });
  const here = $derived(problems.filter((d) => d.line === current && d.severity !== "hint"));
  async function checkProblems() {
    const gen = generation;
    const status = await lspStatus();
    if (!lspUsable(lspStateFor(path, status).state) || destroyed) return;
    checking = true;
    try {
      const found = await problemsFor(path);
      if (gen !== generation || destroyed) return;
      if (meta && found.hash === meta.hash) problems = found.problems;
      // A server that was still reading the project may know more in a moment.
      if (found.indexing) setTimeout(() => gen === generation && !destroyed && void checkProblems(), 4000);
    } catch {
    } finally {
      checking = false;
    }
  }
  function squiggle(from: number, to: number) {
    if (!supportsHighlight) return;
    const registry = (CSS as any).highlights;
    const sets: Record<string, Range[]> = { error: [], warning: [], information: [], hint: [] };
    for (const d of problems) {
      if (d.line < from || d.line > to) continue;
      const text = rows.querySelector(`[data-line="${d.line}"] .code-text`)?.textContent || "";
      let start = Math.min(d.character, text.length),
        end = d.end_line === d.line ? Math.min(d.end_character, text.length) : text.length;
      if (end <= start) {
        // An empty range marks a point: underline the word there, or the rest of the line.
        const w = wordAt(text, start);
        end = w ? w.end : text.length;
        if (w) start = w.start;
      }
      if (end <= start) continue;
      const a = point({ line: d.line, col: start }),
        b = point({ line: d.line, col: end });
      if (!a || !b) continue;
      const range = document.createRange();
      try {
        range.setStart(a[0], a[1]);
        range.setEnd(b[0], b[1]);
        sets[d.severity].push(range);
      } catch {}
    }
    for (const [severity, ranges] of Object.entries(sets)) {
      if (ranges.length) registry.set("lsp-" + severity, new (window as any).Highlight(...ranges));
      else registry.delete("lsp-" + severity);
    }
  }
  function nextProblem(step: 1 | -1) {
    const list = problems.filter((d) => d.severity !== "hint").sort((a, b) => a.line - b.line || a.character - b.character);
    if (!list.length) return onnotice?.("No problems in this file");
    const target = step > 0 ? list.find((d) => d.line > current) ?? list[0] : [...list].reverse().find((d) => d.line < current) ?? list.at(-1)!;
    go(target.line);
    caretCol = target.character;
  }

  // Hover: rest the pointer on a name to see what the language server knows about it (signature,
  // documentation), and the problems reported there.
  let hoverCard = $state<{ x: number; y: number; text: string; problems: Problem[] } | null>(null),
    infoTimer: ReturnType<typeof setTimeout> | undefined,
    infoAt = "";
  const hoverCache = new Map<string, string>();
  function info(event: MouseEvent) {
    if (menu || picker) return;
    const w = wordFromPoint(event.clientX, event.clientY);
    const key = w ? `${w.line}:${w.start}` : "";
    if (key === infoAt) return;
    infoAt = key;
    clearTimeout(infoTimer);
    // Leaving the word closes the card, unless the pointer went onto the card itself.
    hoverCard = null;
    if (!w || !meta) return;
    const at = { x: event.clientX, y: event.clientY },
      hash = meta.hash;
    infoTimer = setTimeout(async () => {
      const marks = (byLine.get(w.line) || []).filter((d) => {
        const end = d.end_line === d.line ? d.end_character : Infinity;
        // A point (empty range) belongs to the word it touches; a span to the words it overlaps.
        return end <= d.character ? w.start <= d.character && d.character <= w.end : w.start < end && w.end > d.character;
      });
      let text = "";
      if (lspUsable(lspStateFor(path, lsp.status).state)) {
        const cacheKey = `${hash}:${key}`;
        text = hoverCache.get(cacheKey) ?? (await hoverAt(path, w.line - 1, w.start, hash));
        if (hoverCache.size > 300) hoverCache.clear();
        hoverCache.set(cacheKey, text);
      }
      if (infoAt !== key || destroyed) return;
      if (text || marks.length) hoverCard = { ...at, text, problems: marks };
    }, 450);
  }

  // The context menu: the editor's "Go to…" actions for the name under the pointer.
  let menu = $state<{ x: number; y: number; word: string } | null>(null);
  function contextMenu(event: MouseEvent) {
    // Some systems turn Ctrl+click into a context-menu click; that one is a definition jump instead.
    if (event.ctrlKey && event.button === 0) return event.preventDefault();
    const w = wordFromPoint(event.clientX, event.clientY);
    if (!w) return;
    event.preventDefault();
    current = w.line;
    caretCol = w.start;
    onposition?.(w.line - 1, w.start, meta?.hash || "");
    schedule();
    hoverCard = null;
    menu = { x: event.clientX, y: event.clientY, word: w.word };
  }
  function act(fn: () => void) {
    menu = null;
    fn();
  }
  // A language server's answer has a column: the name itself is selected there.
  function jumpTo(t: Target) {
    picker = null;
    if (t.path !== path) return onjump?.(t.path, t.line, t.column, t.end);
    onjump?.(path, -1); // remembered for Back, without moving
    if (t.column != null) selectRange({ line: t.line, col: t.column }, { line: t.line, col: t.end ?? t.column });
    else go(t.line);
  }
  /** Goes back to a remembered place in this file (Back and Forward). */
  export function restoreView(v: ViewState) {
    if (!meta) return;
    viewport.scrollTop = v.top;
    viewport.scrollLeft = v.left;
    paint();
    setCaret(v.caret, v.anchor, false);
  }
  function caretWord() {
    const text = rows.querySelector(`[data-line="${current}"] .code-text`)?.textContent || "";
    const w = wordAt(text, caretCol);
    return w ? { line: current, ...w } : null;
  }
  /** F12 and the Definition button: the name at the caret. */
  export function definitionAtCaret() {
    const w = caretWord();
    if (!w) return onnotice?.("Place the caret on a name first");
    const box = rows.querySelector(`[data-line="${current}"]`)?.getBoundingClientRect();
    void goToDefinition(w, { x: (box?.left ?? 0) + 80, y: (box?.bottom ?? 0) });
  }
  export function wordAtCaret() {
    return caretWord()?.word || "";
  }

  onMount(() => {
    void reload();
    const resize = new ResizeObserver(schedule);
    resize.observe(viewport);
    const modifier = (e: KeyboardEvent) => {
      if (e.key === "Control") {
        linking = e.type === "keydown";
        if (!linking) unhover();
      }
    };
    window.addEventListener("keydown", modifier);
    window.addEventListener("keyup", modifier);
    return () => {
      window.removeEventListener("keydown", modifier);
      window.removeEventListener("keyup", modifier);
      unhover();
      destroyed = true;
      controller.abort();
      resize.disconnect();
      cancelAnimationFrame(raf);
      chunks.clear();
    };
  });
</script>

<svelte:window
  onpointerdown={(e) => {
    const t = e.target as Element;
    if (menu && !t.closest?.(".code-menu")) menu = null;
    if (problemList && !t.closest?.(".problem-list, .problem-count")) problemList = false;
  }}
/>
<div class="code-viewer">
  <div class="code-toolbar">
    <div>
      <FileCode2 size={14} /><span>{meta?.language || "Loading source"}</span
      ><span class="subtle">{meta?.total.toLocaleString() || "…"} lines</span
      >{#if disk && disk.path === path && !disk.exists}<span class="disk-note warn"
          >Removed on disk</span
        >{:else if updated}{#key updated}<span class="disk-note">Updated on disk</span>{/key}{/if}
    </div>
    <div>
      <form
        onsubmit={(e) => {
          e.preventDefault();
          go(Number(jump));
        }}
      >
        <Search size={12} /><input
          aria-label="Go to line"
          type="number"
          min="1"
          placeholder="Line…"
          bind:value={jump}
        />
      </form>
      <button
        class="icon-button"
        title="Reload source"
        aria-label="Reload source"
        onclick={reload}><RefreshCw size={14} /></button
      >
    </div>
  </div>
  {#if error}<div class="inline-error">
      {error}<button onclick={reload}>Reload</button>
    </div>{/if}
  {#if findOpen}<div class="find-bar" role="search" in:fly={{ y: -6, duration: 140 }}>
      <input
        bind:this={findInput}
        bind:value={findQuery}
        aria-label="Find in file"
        placeholder="Find"
        class:bad={!!findError || (findQuery && !matches.length)}
        oninput={() => {
          clearTimeout(findTimer);
          findTimer = setTimeout(() => void runFind(), 60);
        }}
        onkeydown={findKey}
      />
      <button class="find-toggle" class:on={findCase} aria-pressed={findCase} title="Match case (Alt+C)" aria-label="Match case" onclick={() => ((findCase = !findCase), void runFind())}><CaseSensitive size={15} /></button>
      <button class="find-toggle" class:on={findWord} aria-pressed={findWord} title="Whole word (Alt+W)" aria-label="Whole word" onclick={() => ((findWord = !findWord), void runFind())}><WholeWord size={15} /></button>
      <button class="find-toggle" class:on={findRegex} aria-pressed={findRegex} title="Regular expression (Alt+R)" aria-label="Regular expression" onclick={() => ((findRegex = !findRegex), void runFind())}><Regex size={15} /></button>
      <span class="find-count" class:bad={!!findError || (findQuery && !matches.length)} aria-live="polite"
        >{findError || (!findQuery ? "" : matches.length ? `${(findIndex + 1).toLocaleString()} of ${matches.length >= 20000 ? "20,000+" : matches.length.toLocaleString()}` : "No results")}</span
      >
      <button class="icon-button" title="Previous match (Shift+Enter)" aria-label="Previous match" disabled={!matches.length} onclick={() => step(-1)}><ArrowUp size={14} /></button>
      <button class="icon-button" title="Next match (Enter)" aria-label="Next match" disabled={!matches.length} onclick={() => step(1)}><ArrowDown size={14} /></button>
      <button class="icon-button" title="Close (Esc)" aria-label="Close find" onclick={closeFind}><X size={14} /></button>
    </div>{/if}
  <!-- svelte-ignore a11y_no_noninteractive_tabindex a11y_no_static_element_interactions -->
  <div
    class="code-viewport"
    bind:this={viewport}
    role="textbox"
    aria-readonly="true"
    tabindex="0"
    onscroll={() => {
      hoverCard = null;
      schedule();
    }}
    onmousedown={pointerDown}
    oncopy={(e) => {
      e.preventDefault();
      void copy();
    }}
    onkeydown={key}
    onmousemove={hover}
    onmouseleave={(e) => {
      unhover();
      clearTimeout(infoTimer);
      infoAt = "";
      if (!(e.relatedTarget as Element | null)?.closest?.(".lsp-hover")) hoverCard = null;
    }}
    oncontextmenu={contextMenu}
    class:linking
    class:finding
    aria-label={`Source code: ${path}`}
  >
    <div
      class="code-sizer"
      bind:this={sizer}
      style:height={`${(meta?.total || 1) * LH + 120}px`}
      style:min-width={`${Math.min(meta?.max_columns || 80, 20000) * 7.8 + 90}px`}
    >
      <div class="code-rows" bind:this={rows}></div>
      <div class="code-caret" bind:this={caretEl} aria-hidden="true"></div>
    </div>
  </div>
  {#if peek && !picker}<div class="def-peek" style:left="{peek.x + 12}px" style:top="{peek.y + 18}px" in:fly={{ y: 4, duration: 120 }}>
      <code>{peek.targets[0].signature || peek.name}</code>
      <span>{peek.targets[0].path}:{peek.targets[0].line}{peek.targets.length > 1 ? ` · +${peek.targets.length - 1} more` : ""}</span>
    </div>{/if}
  {#if hoverCard && !peek && !picker && !menu}<!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      class="lsp-hover"
      style:left="{Math.min(hoverCard.x + 8, innerWidth - 500)}px"
      style:top="{hoverCard.y + 16}px"
      onmouseleave={() => (hoverCard = null)}
      in:fly={{ y: 4, duration: 120 }}
    >
      {#each hoverCard.problems as d}<div class="hover-problem" data-severity={d.severity}>
          <b>{d.severity}</b> {d.message}{#if d.source}<span>{d.source}{d.code != null && d.code !== "" ? `(${d.code})` : ""}</span>{/if}
        </div>{/each}
      {#if hoverCard.text}<div class="hover-doc"><Markdown source={hoverCard.text} compact /></div>{/if}
    </div>{/if}
  {#if menu}<div class="def-picker code-menu" role="menu" tabindex="-1" style:left="{Math.min(menu.x, innerWidth - 260)}px" style:top="{Math.min(menu.y, innerHeight - 240)}px" in:fly={{ y: 4, duration: 100 }}>
      <div class="def-picker-head"><b>{menu.word}</b></div>
      <button role="menuitem" use:focusFirst={true} onclick={() => act(definitionAtCaret)}><span>Go to definition</span><kbd>F12</kbd></button>
      <button role="menuitem" onclick={() => act(() => lookAtCaret("textDocument/typeDefinition"))}><span>Go to type definition</span></button>
      <button role="menuitem" onclick={() => act(() => lookAtCaret("textDocument/implementation"))}><span>Go to implementations</span><kbd>Ctrl+F12</kbd></button>
      <button role="menuitem" onclick={() => act(() => onreferences?.())}><span>Find all references</span><kbd>Shift+F12</kbd></button>
      <button role="menuitem" onclick={() => act(() => void anchor())}><span>Copy path and line</span><kbd>Alt+E</kbd></button>
    </div>{/if}
  {#if problemList}<div class="def-picker problem-list" role="listbox" tabindex="-1" style:right="16px" style:bottom="44px" in:fly={{ y: 4, duration: 120 }}>
      <div class="def-picker-head">{problems.length} problem{problems.length === 1 ? "" : "s"} in this file{checking ? " · checking…" : ""}</div>
      {#each [...problems].sort((a, b) => a.line - b.line || rank[a.severity] - rank[b.severity]) as d}<button
          role="option"
          aria-selected="false"
          data-severity={d.severity}
          onclick={() => {
            problemList = false;
            go(d.line);
            caretCol = d.character;
          }}><code><i class="sev-dot"></i>{d.message}</code><span>Ln {d.line}, Col {d.character + 1}{d.source ? " · " + d.source : ""}</span></button
        >{/each}
    </div>{/if}
  {#if picker}<!-- svelte-ignore a11y_interactive_supports_focus -->
  <div class="def-picker" role="listbox" tabindex="-1" onkeydown={(e) => {
      if (e.key === "Escape") { picker = null; viewport.focus(); }
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const items = [...(e.currentTarget as HTMLElement).querySelectorAll("button")];
        const i = items.indexOf(document.activeElement as HTMLButtonElement);
        items[(i + (e.key === "ArrowDown" ? 1 : items.length - 1)) % items.length]?.focus();
      }
    }} aria-label={"Definitions of " + picker.name} style:left="{Math.min(picker.x, innerWidth - 440)}px" style:top="{Math.min(picker.y + 14, innerHeight - 260)}px" in:fly={{ y: 4, duration: 120 }}>
      <div class="def-picker-head">{picker.targets.length} {picker.kind === "implementation" ? "implementations" : picker.kind === "type definition" ? "type definitions" : "definitions"} of <b>{picker.name}</b></div>
      {#each picker.targets as t, i}<button role="option" aria-selected="false" onclick={() => jumpTo(t)} use:focusFirst={i === 0}
          ><code>{t.signature || t.text?.trim() || t.path}</code><span>{t.external ? "outside the folder: " : ""}{t.path.replace(/^~ext\/[^/]+\//, "")}:{t.line}</span></button
        >{/each}
    </div>{/if}
  <div class="code-foot">
    <span>Ln {current}, Col {column}{selectionLabel ? ` (${selectionLabel})` : ""}</span><span
      class="foot-hint"
      class:foot-problem={here.length}
      data-severity={here[0]?.severity}
      title={here.map((d) => d.message).join("\n")}
      >{finding ? "Looking it up…" : here.length ? here[0].message : "Ctrl+click a name to go to its definition · right-click for more"}</span
    >{#if problems.length || checking}<button
        class="problem-count"
        title="Problems in this file (F8 goes to the next)"
        aria-expanded={problemList}
        onclick={() => (problemList = !problemList)}
        ><i class="sev-dot" data-severity="error"></i>{counts.error}<i class="sev-dot" data-severity="warning"></i>{counts.warning}{#if counts.other}<i class="sev-dot" data-severity="information"></i>{counts.other}{/if}</button
      >{/if}<LspStatus {path} /><button onclick={copy} title="Copy the selection or the current line (Ctrl+C)"><Copy size={12} />Copy</button><button
      onclick={anchor}
      title="Copy path:line (Alt+E)">{path.split("/").at(-1)}:{current}</button
    >
  </div>
</div>

<style>
  .code-viewer {
    position: relative;
    height: 100%;
    min-height: 300px;
    display: flex;
    flex-direction: column;
    background: var(--editor);
  }
  .code-toolbar,
  .code-foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 14px;
    padding: 9px 15px;
    border-bottom: 1px solid var(--line);
    font-size: 11px;
    color: var(--muted);
  }
  .disk-note {
    color: var(--accent);
    animation: disk-note 3s ease forwards;
  }
  .disk-note.warn {
    color: var(--warning);
    animation: none;
  }
  @keyframes disk-note {
    0%,
    70% {
      opacity: 1;
    }
    100% {
      opacity: 0.35;
    }
  }
  .code-toolbar > div {
    display: flex;
    gap: 12px;
    align-items: center;
  }
  .code-toolbar form {
    display: flex;
    align-items: center;
    gap: 5px;
  }
  .code-toolbar input {
    width: 75px;
    padding: 4px 6px;
    font-size: 11px;
  }
  .code-viewport {
    position: relative;
    flex: 1;
    overflow: auto;
    contain: strict;
    outline: none;
    min-height: 250px;
  }
  .code-sizer {
    position: relative;
    min-width: 100%;
  }
  .code-rows {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    will-change: transform;
    font: 13px/22px var(--font-mono);
    font-variant-ligatures: none;
    tab-size: 4;
  }
  .code-foot {
    justify-content: flex-start;
    border-top: 1px solid var(--line);
    border-bottom: 0;
  }
  .code-foot span:nth-child(2) {
    margin-right: auto;
  }
  .code-foot button {
    padding: 3px;
    background: none;
    border: 0;
    display: flex;
    gap: 5px;
    align-items: center;
    font-size: 11px;
  }
  .code-viewport.linking {
    cursor: pointer;
  }
  .code-viewport.finding {
    cursor: progress;
  }
  :global(::highlight(lsp-error)) {
    text-decoration: underline wavy var(--danger);
    text-decoration-skip-ink: none;
    text-underline-offset: 3px;
  }
  :global(::highlight(lsp-warning)) {
    text-decoration: underline wavy var(--warning);
    text-decoration-skip-ink: none;
    text-underline-offset: 3px;
  }
  :global(::highlight(lsp-information)) {
    text-decoration: underline dotted var(--info);
    text-underline-offset: 3px;
  }
  :global(::highlight(lsp-hint)) {
    text-decoration: underline dotted var(--subtle);
    text-underline-offset: 3px;
  }
  :global(.code-row.has-error .code-gutter) {
    color: var(--danger);
    box-shadow: inset 2px 0 var(--danger);
  }
  :global(.code-row.has-warning .code-gutter) {
    color: var(--warning);
    box-shadow: inset 2px 0 var(--warning);
  }
  .sev-dot {
    display: inline-block;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    margin: 0 4px 0 8px;
    background: var(--info);
    vertical-align: 0;
  }
  .sev-dot[data-severity="error"],
  [data-severity="error"] > code > .sev-dot {
    background: var(--danger);
  }
  .sev-dot[data-severity="warning"],
  [data-severity="warning"] > code > .sev-dot {
    background: var(--warning);
  }
  [data-severity="hint"] > code > .sev-dot {
    background: var(--subtle);
  }
  .problem-count {
    display: inline-flex;
    align-items: center;
    color: var(--muted);
  }
  .problem-count .sev-dot:first-child {
    margin-left: 0;
  }
  .foot-hint.foot-problem {
    color: var(--text);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
    flex: 1;
  }
  .foot-hint.foot-problem[data-severity="error"] {
    color: var(--danger);
  }
  .foot-hint.foot-problem[data-severity="warning"] {
    color: var(--warning);
  }
  .problem-list {
    position: fixed;
    left: auto;
    top: auto;
    width: 520px;
    max-width: calc(100vw - 32px);
    max-height: 320px;
  }
  .problem-list code {
    white-space: normal;
  }
  .code-menu {
    width: 250px;
  }
  .code-menu button {
    flex-direction: row;
    justify-content: space-between;
    align-items: center;
    color: var(--text);
  }
  .code-menu kbd {
    font: 10px var(--font-mono);
    color: var(--subtle);
  }
  .lsp-hover {
    position: fixed;
    z-index: 50;
    width: max-content;
    max-width: min(520px, calc(100vw - 32px));
    max-height: 320px;
    overflow: auto;
    border: 1px solid var(--line-strong);
    border-radius: 7px;
    background: var(--surface-raised);
    box-shadow: 0 14px 36px var(--shadow);
    font-size: 12px;
    color: var(--text);
  }
  .hover-problem {
    padding: 8px 12px;
    border-bottom: 1px solid var(--line);
    line-height: 1.45;
  }
  .hover-problem b {
    text-transform: capitalize;
    margin-right: 4px;
  }
  .hover-problem[data-severity="error"] b {
    color: var(--danger);
  }
  .hover-problem[data-severity="warning"] b {
    color: var(--warning);
  }
  .hover-problem span {
    margin-left: 6px;
    color: var(--subtle);
    font-size: 11px;
  }
  .hover-doc {
    padding: 4px 12px;
  }
  .hover-doc :global(pre.md-code) {
    margin: 4px 0;
    padding: 0;
    border: 0;
    background: none;
    white-space: pre-wrap;
    font: 12px/1.5 var(--font-mono);
  }
  .hover-doc :global(hr) {
    margin: 6px 0;
    border: 0;
    border-top: 1px solid var(--line);
  }
  .hover-doc :global(p) {
    margin: 4px 0;
  }
  :global(::highlight(definition-link)) {
    color: var(--accent);
    text-decoration: underline;
    text-underline-offset: 3px;
  }
  .foot-hint {
    color: var(--subtle);
    margin-right: auto;
  }
  .def-peek,
  .def-picker {
    position: fixed;
    z-index: 50;
    max-width: 440px;
    border: 1px solid var(--line-strong);
    border-radius: 7px;
    background: var(--surface-raised);
    box-shadow: 0 14px 36px var(--shadow);
    font-size: 11px;
  }
  .def-peek {
    pointer-events: none;
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 8px 10px;
    color: var(--subtle);
  }
  .def-peek code,
  .def-picker code {
    font: 12px var(--font-mono);
    color: var(--text);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .def-picker {
    padding: 4px;
    width: 440px;
    max-height: 250px;
    overflow: auto;
  }
  .def-picker-head {
    padding: 6px 8px;
    color: var(--subtle);
  }
  .def-picker button {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    width: 100%;
    padding: 6px 8px;
    border-radius: 5px;
    text-align: left;
    color: var(--subtle);
  }
  .def-picker button:hover,
  .def-picker button:focus-visible {
    background: var(--surface-hover);
    outline: none;
  }
  :global(.code-row) {
    display: flex;
    height: 22px;
    white-space: pre;
    min-width: 100%;
    color: var(--text);
  }
  :global(.code-row.current) {
    background: var(--surface-raised);
  }
  /* The selection: painted over the text (the Highlight API), the line break of each fully crossed
     line as a small block, and whole rows where the Highlight API is missing. */
  :global(::highlight(code-selection)) {
    background-color: var(--selection);
  }
  :global(.code-row.sel-eol .code-text::after) {
    content: "";
    display: inline-block;
    width: 0.6ch;
    height: 22px;
    vertical-align: top;
    background: var(--selection);
  }
  :global(.code-row.sel-fallback) {
    background: var(--selection);
  }
  /* The browser's own selection never shows in code: Falcon draws its own. */
  :global(.code-rows ::selection) {
    background: transparent;
  }
  .code-rows {
    user-select: none;
    -webkit-user-select: none;
  }
  /* Find in file */
  .find-bar {
    position: absolute;
    top: 46px;
    right: 22px;
    z-index: 6;
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 4px 4px 4px 6px;
    border: 1px solid var(--line-strong);
    border-radius: 8px;
    background: var(--surface-raised);
    box-shadow: 0 10px 28px var(--shadow);
  }
  .find-bar input {
    width: 220px;
    height: 26px;
    padding: 0 8px;
    border: 1px solid var(--line-strong);
    border-radius: 5px;
    background: var(--input);
    font: 12px var(--font-mono);
    color: var(--bright);
  }
  .find-bar input:focus {
    border-color: var(--accent);
    outline: none;
  }
  .find-bar input.bad {
    border-color: var(--danger);
  }
  .find-toggle {
    display: grid;
    place-items: center;
    width: 26px;
    height: 26px;
    border-radius: 5px;
    color: var(--subtle);
  }
  .find-toggle:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  .find-toggle.on {
    background: var(--accent-soft, var(--surface-hover));
    color: var(--accent);
    box-shadow: inset 0 0 0 1px var(--accent);
  }
  .find-count {
    min-width: 78px;
    padding: 0 6px;
    font-size: 11px;
    color: var(--muted);
    text-align: center;
    white-space: nowrap;
  }
  .find-count.bad {
    color: var(--danger);
  }
  :global(::highlight(find-match)) {
    background-color: var(--find-match, rgba(234, 179, 8, 0.32));
  }
  :global(::highlight(find-current)) {
    background-color: var(--find-current, rgba(249, 115, 22, 0.55));
  }
  .code-caret {
    position: absolute;
    top: 0;
    left: 0;
    width: 2px;
    height: 22px;
    margin-left: -1px;
    background: var(--accent);
    pointer-events: none;
    display: none;
    opacity: 0;
    z-index: 2;
  }
  .code-viewport:focus .code-caret,
  .code-viewport:focus-within .code-caret {
    opacity: 1;
  }
  .code-caret:global(.blink) {
    animation: caret-blink 1.06s steps(1) 0.5s infinite;
  }
  @keyframes caret-blink {
    50% {
      opacity: 0;
    }
  }
  :global(.code-gutter) {
    position: sticky;
    left: 0;
    width: 58px;
    flex-shrink: 0;
    text-align: right;
    padding-right: 18px;
    background: var(--editor);
    color: var(--subtle);
    user-select: none;
    z-index: 1;
  }
  :global(.code-text) {
    display: block;
    padding-right: 28px;
  }
  :global(.code-text i) {
    font-style: normal;
  }
  /* Token colours: keywords, types and calls stand apart from names; strings and numbers are warm,
     comments recede. Shared with the editor and diffs (global). */
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .k) {
    color: var(--syn-keyword);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .kt) {
    color: var(--syn-type);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .s) {
    color: var(--syn-string);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) :is(.m, .no)) {
    color: var(--syn-number);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) :is(.c, .cp)) {
    color: var(--syn-comment);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .nf) {
    color: var(--syn-function);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .nc) {
    color: var(--syn-class);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .nb) {
    color: var(--syn-builtin);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .nd) {
    color: var(--syn-decorator);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .np) {
    color: var(--syn-property);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .o) {
    color: var(--syn-operator);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .nt) {
    color: var(--syn-builtin);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .na) {
    color: var(--syn-attribute);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .gi) {
    color: var(--success);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .gd) {
    color: var(--danger);
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .gh) {
    color: var(--bright);
    font-weight: 600;
  }
  :global(:is(.code-text, .git-diff-row code, .diff-row code, .editor-highlight) .err) {
    text-decoration: underline wavy var(--danger);
  }
</style>
