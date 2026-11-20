<script lang="ts">
  // A small minimap beside the code: each line drawn as a bar of its indent and length (no text, so it
  // costs nothing to draw however long the file is), with the visible region as a box, and problems,
  // find matches and the caret's line marked. Click or drag to scroll. A long file is shown whole: each
  // pixel row stands for several lines (the longest of them).
  import { api } from "./api";
  let {
    path,
    hash,
    total,
    viewport,
    lineHeight,
    marks = [],
  }: {
    path: string;
    hash: string;
    total: number;
    viewport: HTMLElement | undefined;
    lineHeight: number;
    /** Lines to mark: problems, find matches, the caret's line. */
    marks?: { line: number; kind: "error" | "warning" | "find" | "current" }[];
  } = $props();
  const WIDTH = 48;
  const MAX_ROW = 2; // pixels a line at most (a short file)
  let canvas = $state<HTMLCanvasElement>(),
    shape: Uint8Array | null = null,
    loadedFor = "",
    height = $state(0),
    top = $state(0),
    span = $state(0);

  async function load() {
    const key = path + "\u0000" + hash;
    if (!hash || key === loadedFor) return;
    loadedFor = key;
    try {
      const r = await api<{ hash: string; shape: string }>("/api/code/shape?path=" + encodeURIComponent(path));
      if (loadedFor !== key) return;
      const bin = atob(r.shape);
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      shape = bytes;
      draw();
    } catch {
      shape = null;
    }
  }
  $effect(() => {
    void path;
    void hash;
    void load();
  });

  // How many pixels a line takes: at most MAX_ROW, less when the file is taller than the minimap.
  const scale = $derived(total > 0 && height > 0 ? Math.min(MAX_ROW, height / total) : MAX_ROW);

  function color(name: string, fallback: string) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
  }
  function draw() {
    if (!canvas || !shape) return;
    const dpr = window.devicePixelRatio || 1;
    const h = Math.max(1, height);
    canvas.width = WIDTH * dpr;
    canvas.height = h * dpr;
    const ctx = canvas.getContext("2d")!;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, WIDTH, h);
    const lines = shape.length / 2;
    const s = Math.min(MAX_ROW, h / Math.max(1, lines));
    const cols = WIDTH - 6; // a column of marks on the right
    const unit = cols / 120; // 120 columns fill the width
    ctx.fillStyle = color("--text", "#888");
    ctx.globalAlpha = 0.32;
    if (s >= 1) {
      for (let i = 0; i < lines; i++) {
        const indent = shape[i * 2],
          len = shape[i * 2 + 1];
        if (!len) continue;
        ctx.fillRect(2 + indent * unit, i * s, Math.max(1, (len - indent) * unit), Math.max(1, s - 0.4));
      }
    } else {
      // Several lines a pixel row: the longest of them, from the smallest indent.
      for (let y = 0; y < h; y++) {
        const from = Math.floor(y / s),
          to = Math.min(lines, Math.floor((y + 1) / s));
        let indent = 255,
          len = 0;
        for (let i = from; i < to; i++) {
          if (shape[i * 2 + 1]) {
            indent = Math.min(indent, shape[i * 2]);
            len = Math.max(len, shape[i * 2 + 1]);
          }
        }
        if (len) ctx.fillRect(2 + indent * unit, y, Math.max(1, (len - indent) * unit), 1);
      }
    }
    ctx.globalAlpha = 1;
    const tint: Record<string, string> = {
      error: color("--danger", "#e5484d"),
      warning: color("--warning", "#d4a017"),
      find: color("--find-mark", "#f59e0b"),
      current: color("--accent", "#7856bf"),
    };
    for (const m of marks) {
      const y = Math.min(h - 2, (m.line - 1) * s);
      ctx.fillStyle = tint[m.kind];
      if (m.kind === "current") ctx.fillRect(0, y, WIDTH, Math.max(1, s));
      else if (m.kind === "find") ctx.fillRect(WIDTH - 9, y, 4, Math.max(2, s));
      else ctx.fillRect(WIDTH - 4, y, 4, Math.max(2, s));
    }
  }
  $effect(() => {
    void marks;
    void height;
    draw();
  });

  // The box: the lines on screen.
  function track() {
    if (!viewport) return;
    span = Math.max(8, (viewport.clientHeight / lineHeight) * scale);
    // The box stays inside the minimap (its least height can carry it past the end).
    top = Math.min((viewport.scrollTop / lineHeight) * scale, Math.max(0, Math.min(height, total * scale) - span));
  }
  $effect(() => {
    const v = viewport;
    if (!v) return;
    let frame = 0;
    const onScroll = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(track);
    };
    v.addEventListener("scroll", onScroll, { passive: true });
    const resize = new ResizeObserver(() => {
      height = canvas?.parentElement?.clientHeight || 0;
      track();
    });
    if (canvas?.parentElement) resize.observe(canvas.parentElement);
    // The theme changes the colours.
    const theme = new MutationObserver(() => draw());
    theme.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    const scheme = matchMedia("(prefers-color-scheme: dark)");
    scheme.addEventListener("change", draw);
    track();
    return () => {
      v.removeEventListener("scroll", onScroll);
      resize.disconnect();
      theme.disconnect();
      scheme.removeEventListener("change", draw);
      cancelAnimationFrame(frame);
    };
  });
  $effect(() => {
    void scale;
    track();
  });

  // Click to centre that place; drag to scroll.
  function scrollTo(y: number) {
    if (!viewport) return;
    const line = y / scale;
    viewport.scrollTop = Math.max(0, line * lineHeight - viewport.clientHeight / 2);
  }
  function down(e: PointerEvent) {
    if (e.button !== 0) return;
    e.preventDefault();
    const el = e.currentTarget as HTMLElement;
    el.setPointerCapture(e.pointerId);
    const base = el.getBoundingClientRect().top;
    const inBox = e.clientY - base >= top && e.clientY - base <= top + span;
    const grab = inBox ? e.clientY - base - top - span / 2 : 0;
    scrollTo(e.clientY - base - grab);
    const move = (ev: PointerEvent) => scrollTo(ev.clientY - base - grab);
    const up = () => {
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
    };
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="minimap" style:width="{WIDTH}px" onpointerdown={down} aria-hidden="true">
  <canvas bind:this={canvas} style:width="{WIDTH}px" style:height="{height}px"></canvas>
  <div class="minimap-box" style:transform="translateY({top}px)" style:height="{span}px"></div>
</div>

<style>
  .minimap {
    position: relative;
    flex: none;
    height: 100%;
    overflow: hidden;
    cursor: default;
    background: var(--editor);
    border-left: 1px solid var(--line);
  }
  canvas {
    display: block;
  }
  .minimap-box {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    background: var(--scroll-thumb);
    opacity: 0.5;
    pointer-events: none;
  }
  .minimap:hover .minimap-box {
    opacity: 0.8;
  }
  @media (max-width: 760px) {
    .minimap {
      display: none;
    }
  }
</style>
