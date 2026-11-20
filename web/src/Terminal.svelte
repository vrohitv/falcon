<script lang="ts">
  // One terminal: a session's screen (xterm.js, drawn by the GPU when it can), its typing, its size.
  // Output is acknowledged once drawn, so a program printing a lot is paused by the server instead of
  // flooding the page. After a reconnect it attaches again and the server sends the screen back.
  import { onMount } from "svelte";
  import { Terminal } from "@xterm/xterm";
  import { FitAddon } from "@xterm/addon-fit";
  import { WebglAddon } from "@xterm/addon-webgl";
  import { SearchAddon } from "@xterm/addon-search";
  import { Unicode11Addon } from "@xterm/addon-unicode11";
  import "@xterm/xterm/css/xterm.css";
  import { send, sendInput, sendBytes, onOutput, on, onOpen } from "./socket.svelte";
  let {
    id,
    folder,
    visible = true,
    onlink,
    onexit,
    onfocus,
  }: {
    id: string;
    /** The folder's path on disk (an absolute path inside it opens as a folder path). */
    folder: string;
    visible?: boolean;
    /** Ctrl+click on path:line in the output. */
    onlink: (path: string, line: number, column: number) => void;
    /** The shell exited (code), or its session is gone (null). */
    onexit: (code: number | null) => void;
    onfocus?: () => void;
  } = $props();
  let host: HTMLDivElement;
  let term: Terminal | undefined, fit: FitAddon | undefined, search: SearchAddon | undefined;

  export function focus() {
    term?.focus();
  }
  export function find(q: string, back = false) {
    if (!search || !q) return false;
    const opts = { decorations: { matchOverviewRuler: "#888", activeMatchColorOverviewRuler: "#f59e0b", matchBackground: "#f59e0b55", activeMatchBackground: "#f59e0bcc" } };
    return back ? search.findPrevious(q, opts) : search.findNext(q, opts);
  }
  export function clear() {
    term?.clear();
  }

  // Colours follow Falcon's theme (and change with it).
  function theme() {
    const css = getComputedStyle(document.documentElement);
    const v = (n: string, d: string) => css.getPropertyValue(n).trim() || d;
    const dark = (css.getPropertyValue("color-scheme").trim() || "") !== "light" && v("--editor", "#1e1e1e").toLowerCase() < "#808080";
    const ansi = dark
      ? ["#1e1e1e", "#f14c4c", "#23d18b", "#e5e510", "#3b8eea", "#d670d6", "#29b8db", "#cccccc", "#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#e5e5e5"]
      : ["#000000", "#cd3131", "#00bc00", "#949800", "#0451a5", "#bc05bc", "#0598bc", "#555555", "#666666", "#cd3131", "#14ce14", "#b5ba00", "#0451a5", "#bc05bc", "#0598bc", "#a5a5a5"];
    const [black, red, green, yellow, blue, magenta, cyan, white, brightBlack, brightRed, brightGreen, brightYellow, brightBlue, brightMagenta, brightCyan, brightWhite] = ansi;
    return {
      background: v("--editor", dark ? "#1e1e1e" : "#ffffff"),
      foreground: v("--text", dark ? "#cccccc" : "#333333"),
      cursor: v("--accent", "#7856bf"),
      cursorAccent: v("--editor", "#1e1e1e"),
      selectionBackground: v("--selection", "#264f78"),
      black, red, green, yellow, blue, magenta, cyan, white,
      brightBlack, brightRed, brightGreen, brightYellow, brightBlue, brightMagenta, brightCyan, brightWhite,
    };
  }

  // A path in the output (src/app.ts:12:5, ./main.go:40, C:\code\x\y.py:3, /abs/in/folder/z.rs:9) opens
  // in the editor on Ctrl+click.
  const pathRe = /((?:[A-Za-z]:[\\/]|\.{0,2}[\\/])?(?:[\w.@~+-]+[\\/])*[\w.@~+-]+\.[A-Za-z0-9]{1,12})(?::(\d+))?(?::(\d+))?/g;
  function toFolderPath(p: string): string | null {
    let path = p.replace(/\\/g, "/");
    const root = folder.replace(/\\/g, "/").replace(/\/$/, "");
    if (path.toLowerCase().startsWith(root.toLowerCase() + "/")) path = path.slice(root.length + 1);
    else if (/^([A-Za-z]:)?\//.test(path)) return null; // outside the folder
    return path.replace(/^\.\//, "");
  }

  onMount(() => {
    const css = getComputedStyle(document.documentElement);
    term = new Terminal({
      fontFamily: css.getPropertyValue("--font-mono").trim() || "monospace",
      fontSize: 13,
      lineHeight: 1.15,
      cursorBlink: true,
      scrollback: 10000,
      allowProposedApi: true,
      theme: theme(),
      smoothScrollDuration: 0,
      rightClickSelectsWord: false,
      // On Windows the shell runs on the pseudo-console, which reflows lines itself.
      ...(navigator.userAgent.includes("Windows") ? { windowsPty: { backend: "conpty" as const, buildNumber: 19045 } } : {}),
    });
    fit = new FitAddon();
    search = new SearchAddon();
    term.loadAddon(fit);
    term.loadAddon(search);
    const unicode = new Unicode11Addon();
    term.loadAddon(unicode);
    term.unicode.activeVersion = "11";
    term.open(host);
    // The screen is drawn on a canvas; tests (and the browser's tools) read its text from here.
    Object.defineProperty(host, "falconTerminal", { value: term, configurable: true });
    // The GPU renderer when the browser allows it; the built-in one otherwise (or after losing it).
    try {
      const gl = new WebglAddon();
      gl.onContextLoss(() => gl.dispose());
      term.loadAddon(gl);
    } catch {}
    fit.fit();

    // Output: drawn, then acknowledged in batches.
    let unacked = 0,
      ackTimer: ReturnType<typeof setTimeout> | undefined;
    const flush = () => {
      clearTimeout(ackTimer);
      ackTimer = undefined;
      if (unacked) send({ t: "term.ack", id, bytes: unacked });
      unacked = 0;
    };
    const offOutput = onOutput(id, (data) => {
      const n = data.length;
      term!.write(data, () => {
        unacked += n;
        if (unacked >= 256 << 10) flush();
        else ackTimer ??= setTimeout(flush, 30);
      });
    });
    // Attach now and after every reconnect: the server sends the screen first, so start clean.
    let attached = false;
    const offOpen = onOpen(() => {
      if (attached) term!.reset();
      attached = true;
      send({ t: "term.attach", id, cols: term!.cols, rows: term!.rows });
    });
    const offExit = on("term.exit", (m) => {
      if (m.id !== id) return;
      onexit(m.code);
    });
    const offGone = on("term.gone", (m) => m.id === id && onexit(null));

    term.onData((d) => sendInput(id, d));
    term.onBinary((d) => sendBytes(id, Uint8Array.from(d, (c) => c.charCodeAt(0) & 255)));
    let resizeTimer: ReturnType<typeof setTimeout> | undefined;
    term.onResize(({ cols, rows }) => {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(() => send({ t: "term.resize", id, cols, rows }), 40);
    });
    term.textarea?.addEventListener("focus", () => onfocus?.());

    // Shortcuts that belong to Falcon, not the shell (as VS Code keeps Ctrl+P and the like): the
    // terminal ignores them and Falcon handles them. Ctrl+C copies when there is a selection; Ctrl+V and
    // Ctrl+Shift+V paste; Ctrl+Shift+C copies.
    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown") return true;
      const k = e.key.toLowerCase();
      if (e.ctrlKey && (k === "`" || e.code === "Backquote")) return false;
      if (e.ctrlKey && !e.altKey && (k === "\\" || e.code === "Backslash")) return false;
      if (e.ctrlKey && !e.shiftKey && !e.altKey && (k === "p" || /^[1-4]$/.test(k))) return false;
      if (e.ctrlKey && e.shiftKey && ["f", "e", "g", "o", "p"].includes(k)) return false;
      if (e.ctrlKey && k === "c" && (e.shiftKey || term!.hasSelection())) {
        void navigator.clipboard?.writeText(term!.getSelection());
        if (!e.shiftKey) term!.clearSelection();
        e.preventDefault();
        return false;
      }
      if (e.ctrlKey && k === "v") {
        // The browser pastes into the terminal (its paste event); the key itself is not sent.
        return false;
      }
      return true;
    });

    // Ctrl+click on a path opens it.
    term.registerLinkProvider({
      provideLinks(y, callback) {
        const line = term!.buffer.active.getLine(y - 1)?.translateToString(true) ?? "";
        const links: any[] = [];
        pathRe.lastIndex = 0;
        for (let m = pathRe.exec(line); m; m = pathRe.exec(line)) {
          const [whole, p, ln, col] = m;
          // A bare word with a dot (v1.2, example.com) is not a path: it needs a folder or a line.
          if (!ln && !/[\\/]/.test(p)) continue;
          const path = toFolderPath(p);
          if (!path) continue;
          links.push({
            range: { start: { x: m.index + 1, y }, end: { x: m.index + whole.length, y } },
            text: whole,
            decorations: { pointerCursor: true, underline: true },
            activate: (e: MouseEvent) => {
              if (e.ctrlKey) onlink(path, Number(ln) || 1, Math.max(0, (Number(col) || 1) - 1));
            },
            hover: () => (host.title = "Ctrl+click to open " + path + (ln ? ":" + ln : "")),
            leave: () => (host.title = ""),
          });
        }
        callback(links);
      },
    });

    const resize = new ResizeObserver(() => {
      if (visible && host.offsetWidth && host.offsetHeight) fit!.fit();
    });
    resize.observe(host);
    const retheme = () => term && (term.options.theme = theme());
    const themeObserver = new MutationObserver(retheme);
    themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    const scheme = matchMedia("(prefers-color-scheme: dark)");
    scheme.addEventListener("change", retheme);
    return () => {
      flush();
      offOutput();
      offOpen();
      offExit();
      offGone();
      send({ t: "term.detach", id });
      resize.disconnect();
      themeObserver.disconnect();
      scheme.removeEventListener("change", retheme);
      clearTimeout(resizeTimer);
      term?.dispose();
    };
  });
  $effect(() => {
    if (visible) requestAnimationFrame(() => host?.offsetWidth && fit?.fit());
  });
</script>

<div class="term-host" bind:this={host}></div>

<style>
  .term-host {
    position: absolute;
    inset: 4px 0 0 10px;
  }
  .term-host :global(.xterm) {
    height: 100%;
  }
  .term-host :global(.xterm-viewport) {
    background: transparent !important;
  }
</style>
