<script lang="ts">
  // Files that are not text: images (zoom, fit, real size), PDF (the browser's own viewer), audio, video,
  // fonts (a type specimen), tables (CSV, TSV) and anything else (what it is, how big, a way to save it).
  import { onDestroy } from "svelte";
  import { apiURL } from "./api";
  import type { Viewer } from "./file-kind";
  import { fade } from "./motion";
  import { ZoomIn, ZoomOut, Maximize, Scan, Download, FileBox } from "@lucide/svelte";
  let {
    path,
    viewer,
    version = 0,
  }: {
    path: string;
    viewer: Viewer;
    /** Goes up when the file changed on disk, to show the new bytes. */
    version?: number;
  } = $props();
  const src = $derived(apiURL("/api/raw?path=" + encodeURIComponent(path)) + "&v=" + version);
  const name = $derived(path.split("/").at(-1) || path);
  let size = $state<number | null>(null),
    natural = $state<{ w: number; h: number } | null>(null),
    zoom = $state<"fit" | number>("fit"),
    broken = $state(false);
  $effect(() => {
    void src;
    natural = null;
    broken = false;
    fetch(src, { method: "HEAD" })
      .then((r) => (size = Number(r.headers.get("content-length")) || null))
      .catch(() => (size = null));
  });
  const bytes = (n: number | null) =>
    n == null ? "" : n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} KB` : `${(n / (1 << 20)).toFixed(1)} MB`;
  function step(by: number) {
    const cur = zoom === "fit" ? 1 : zoom;
    zoom = Math.min(16, Math.max(0.05, Math.round(cur * by * 100) / 100));
  }
  function wheel(e: WheelEvent) {
    if (!(e.ctrlKey || e.metaKey)) return;
    e.preventDefault();
    step(e.deltaY < 0 ? 1.15 : 1 / 1.15);
  }

  // Tables: parsed in the browser (RFC 4180 quoting), the first 5,000 rows shown.
  let table = $state<{ head: string[]; rows: string[][]; total: number } | null>(null),
    tableError = $state("");
  function parse(text: string, sep: string) {
    const rows: string[][] = [];
    let row: string[] = [],
      cell = "",
      quoted = false;
    for (let i = 0; i < text.length; i++) {
      const c = text[i];
      if (quoted) {
        if (c === '"' && text[i + 1] === '"') {
          cell += '"';
          i++;
        } else if (c === '"') quoted = false;
        else cell += c;
      } else if (c === '"' && cell === "") quoted = true;
      else if (c === sep) {
        row.push(cell);
        cell = "";
      } else if (c === "\n" || c === "\r") {
        if (c === "\r" && text[i + 1] === "\n") i++;
        row.push(cell);
        rows.push(row);
        row = [];
        cell = "";
      } else cell += c;
    }
    if (cell || row.length) {
      row.push(cell);
      rows.push(row);
    }
    return rows;
  }
  $effect(() => {
    if (viewer !== "table") return;
    table = null;
    tableError = "";
    fetch(src)
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(r.statusText))))
      .then((text) => {
        const rows = parse(text, path.toLowerCase().endsWith(".tsv") ? "\t" : ",");
        const [head = [], ...rest] = rows;
        table = { head, rows: rest.slice(0, 5000), total: rest.length };
      })
      .catch((e) => (tableError = e.message));
  });

  // Fonts: loaded from the folder under a private name and shown at several sizes.
  let fontFamily = $state(""),
    fontFace: FontFace | null = null;
  $effect(() => {
    if (viewer !== "font") return;
    const family = "falcon-preview-" + Math.random().toString(36).slice(2);
    const face = new FontFace(family, `url("${src}")`);
    face
      .load()
      .then((f) => {
        document.fonts.add(f);
        fontFace = f;
        fontFamily = family;
      })
      .catch(() => (broken = true));
    return () => {
      if (fontFace) document.fonts.delete(fontFace);
    };
  });
  onDestroy(() => {
    if (fontFace) document.fonts.delete(fontFace);
  });
</script>

<div class="media-view" data-viewer={viewer}>
  <div class="media-bar">
    <span class="media-name">{name}</span>
    {#if natural}<span class="media-meta">{natural.w} × {natural.h}</span>{/if}
    {#if table}<span class="media-meta">{table.total.toLocaleString()} rows · {table.head.length} columns</span>{/if}
    {#if size != null}<span class="media-meta">{bytes(size)}</span>{/if}
    <span class="media-gap"></span>
    {#if viewer === "image" || viewer === "svg"}
      <button class="icon-button" title="Zoom out (⌘-scroll)" aria-label="Zoom out" onclick={() => step(1 / 1.25)}><ZoomOut size={15} /></button>
      <span class="media-zoom">{zoom === "fit" ? "Fit" : Math.round(zoom * 100) + "%"}</span>
      <button class="icon-button" title="Zoom in (⌘-scroll)" aria-label="Zoom in" onclick={() => step(1.25)}><ZoomIn size={15} /></button>
      <button class="icon-button" class:on={zoom === "fit"} title="Fit to the window" aria-label="Fit" onclick={() => (zoom = "fit")}><Maximize size={15} /></button>
      <button class="icon-button" class:on={zoom === 1} title="Actual size" aria-label="Actual size" onclick={() => (zoom = 1)}><Scan size={15} /></button>
    {/if}
    <a class="icon-button" href={src} download={name} title="Save a copy" aria-label="Save a copy"><Download size={15} /></a>
  </div>
  <div class="media-body" in:fade={{ duration: 140 }}>
    {#if viewer === "image" || viewer === "svg"}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="media-canvas checker" class:fit={zoom === "fit"} onwheel={wheel}>
        {#if broken}<p class="media-note">This image could not be shown.</p>
        {:else}<img
            {src}
            alt={name}
            style:width={zoom === "fit" || !natural ? null : natural.w * zoom + "px"}
            onload={(e) => {
              const img = e.currentTarget as HTMLImageElement;
              natural = { w: img.naturalWidth, h: img.naturalHeight };
            }}
            onerror={() => (broken = true)}
          />{/if}
      </div>
    {:else if viewer === "pdf"}
      <iframe class="media-pdf" {src} title={name}></iframe>
    {:else if viewer === "video"}
      <!-- svelte-ignore a11y_media_has_caption -->
      <video class="media-video" {src} controls preload="metadata"></video>
    {:else if viewer === "audio"}
      <div class="media-audio"><audio {src} controls preload="metadata"></audio></div>
    {:else if viewer === "font"}
      {#if broken}<p class="media-note">This font could not be loaded.</p>
      {:else if fontFamily}<div class="font-specimen" style:font-family={fontFamily}>
          <p class="font-big">Aa Bb Cc</p>
          <p style="font-size: 32px">The quick brown fox jumps over the lazy dog</p>
          <p style="font-size: 20px">ABCDEFGHIJKLMNOPQRSTUVWXYZ abcdefghijklmnopqrstuvwxyz 0123456789 &amp;@#?!</p>
          <p style="font-size: 14px">Pack my box with five dozen liquor jugs. Sphinx of black quartz, judge my vow.</p>
        </div>{/if}
    {:else if viewer === "table"}
      {#if tableError}<p class="media-note">{tableError}</p>
      {:else if table}<div class="media-table">
          <table>
            <thead><tr><th class="row-n"></th>{#each table.head as h}<th>{h}</th>{/each}</tr></thead>
            <tbody>
              {#each table.rows as row, i}<tr><td class="row-n">{i + 1}</td>{#each row as cell}<td title={cell.length > 60 ? cell : undefined}>{cell}</td>{/each}</tr>{/each}
            </tbody>
          </table>
          {#if table.total > table.rows.length}<p class="media-note">Showing the first {table.rows.length.toLocaleString()} of {table.total.toLocaleString()} rows.</p>{/if}
        </div>{/if}
    {:else}
      <div class="media-binary">
        <FileBox size={40} strokeWidth={1.25} />
        <h3>{name}</h3>
        <p>A binary file{size != null ? `, ${bytes(size)}` : ""}. Falcon shows text, images, PDF, audio, video, fonts and tables.</p>
        <a class="button" href={src} download={name}><Download size={14} />Save a copy</a>
      </div>
    {/if}
  </div>
</div>
