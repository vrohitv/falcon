<script lang="ts">
  import { onMount, tick, untrack } from "svelte";
  import { api, APIError } from "./api";
  import { cachedContent, rememberContent } from "./filecache";
  import { forgetDefinitions } from "./definition";
  import { confirmDiscard } from "./confirm.svelte";
  import { fade, fly } from "./motion";
  import {
    Save,
    Undo2,
    Check,
    X,
    TriangleAlert,
    RefreshCw,
    Copy,
    FileCode2,
  } from "@lucide/svelte";

  // A deliberately plain editor: a native textarea keeps the browser's undo
  // stack, IME composition, selection and find. Its text is transparent over
  // a highlighted copy of the lines on screen, coloured by the server's
  // highlighter. Saves are hash-checked against the version that was loaded,
  // so newer content on disk is never overwritten without an explicit choice.
  let {
    path,
    startLine = 1,
    ondone,
    ondirty,
    onerror,
    disk = null,
  }: {
    path: string;
    startLine?: number;
    disk?: { path: string; hash: string; exists: boolean; n: number } | null;
    ondone: (saved: boolean) => void;
    ondirty: (dirty: boolean) => void;
    onerror: (message: string) => void;
  } = $props();

  const LH = 22;
  let text = $state(""),
    original = $state(""),
    hash = $state(""),
    loading = $state(true),
    loadError = $state(""),
    saving = $state(false),
    savedAt = $state(0),
    conflict = $state(false),
    line = $state(1),
    column = $state(1),
    everSaved = false;
  let area = $state<HTMLTextAreaElement>(),
    gutter = $state<HTMLPreElement>();
  const dirty = $derived(!loading && text !== original);
  // Counted without allocating: these run on every keystroke, and a large
  // file would otherwise be split into an array each time.
  function count(s: string, end = s.length) {
    let n = 1;
    for (let i = s.indexOf("\n"); i !== -1 && i < end; i = s.indexOf("\n", i + 1)) n++;
    return n;
  }
  const lines = $derived(count(text));
  const numbers = $derived(
    Array.from({ length: lines }, (_, i) => i + 1).join("\n"),
  );
  $effect(() => ondirty(dirty));
  let reloadedAt = $state(0);
  // Someone else changed this file. With no local edits, take theirs and keep
  // the caret's line; with local edits, raise the conflict now rather than at
  // save time. Events for our own saves carry our hash and are ignored.
  let handled = 0;
  $effect(() => {
    const event = disk;
    if (!event || event.path !== path || event.n === handled) return;
    handled = event.n;
    untrack(() => {
      // While loading or saving, the response itself carries the latest hash;
      // a real concurrent change surfaces as a refused save.
      if (loading || saving || (event.exists && event.hash === hash)) return;
      if (dirty || !event.exists) {
        conflict = true;
        return;
      }
      const keep = line;
      void load().then(() => {
        goTo(keep);
        reloadedAt = Date.now();
      });
    });
  });

  const unit = $derived.by(() => {
    if (/(^|\/)(Makefile|GNUmakefile)$|\.go$/.test(path)) return "\t";
    let tabs = 0,
      two = 0,
      four = 0;
    for (const l of original.split("\n").slice(0, 2000)) {
      if (l.startsWith("\t")) tabs++;
      else if (/^ {4}\S/.test(l)) four++;
      else if (/^ {2}\S/.test(l)) two++;
    }
    if (tabs > two + four) return "\t";
    return four > two ? "    " : "  ";
  });

  async function apply(file: { hash: string; content: string }) {
    const first = !hash;
    text = original = file.content;
    hash = file.hash;
    conflict = false;
    loading = false;
    await tick();
    if (first) goTo(startLine);
  }
  async function fetchFile() {
    const file = await api<{ hash: string; content: string }>(
      "/api/file?path=" + encodeURIComponent(path),
    );
    rememberContent(path, file);
    return file;
  }
  async function load() {
    loading = true;
    loadError = "";
    try {
      // First open of a file seen before: edit the cached copy at once and
      // confirm its hash; a moved file is fetched before any keystroke lands
      // on the wrong version, and saves are hash-checked regardless.
      const cached = !hash ? cachedContent(path) : undefined;
      if (cached) {
        await apply(cached);
        const stat = await api<{ hash: string; exists: boolean }>(
          "/api/file/stat?path=" + encodeURIComponent(path),
        );
        if (stat.hash !== cached.hash && text === original) await apply(await fetchFile());
        else if (stat.hash !== cached.hash) conflict = true;
        return;
      }
      await apply(await fetchFile());
    } catch (e) {
      loadError = (e as Error).message;
      loading = false;
    }
  }

  function goTo(n: number) {
    if (!area) return;
    const target = Math.max(1, Math.min(n, lines));
    let offset = 0;
    for (let i = 1, idx = 0; i < target; i++) {
      idx = text.indexOf("\n", idx);
      if (idx < 0) break;
      offset = ++idx;
    }
    area.focus();
    area.setSelectionRange(offset, offset);
    area.scrollTop = Math.max(0, (target - 4) * LH);
    caret();
  }

  function caret() {
    if (!area) return;
    const at = area.selectionStart;
    line = count(text, at);
    column = at - (at > 0 ? text.lastIndexOf("\n", at - 1) : -1);
  }

  // Highlighting: only the lines on screen are drawn. Colours come back from
  // the server keyed by line text, so a line that moved keeps its colours and
  // a line just typed shows plain until its colours arrive.
  let scrollTop = $state(0),
    scrollLeft = $state(0),
    viewHeight = $state(600);
  const colours = new Map<string, string>();
  let painted = $state(0);
  const rows = $derived(text.split("\n"));
  const first = $derived(Math.max(0, Math.floor(scrollTop / LH) - 10));
  const last = $derived(Math.min(rows.length, first + Math.ceil(viewHeight / LH) + 20));
  const escape = (s: string) =>
    s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  const visible = $derived.by(() => {
    void painted;
    const out: string[] = [];
    for (let i = first; i < last; i++) out.push(colours.get(rows[i]) ?? escape(rows[i]));
    return out;
  });
  let colourTimer: ReturnType<typeof setTimeout> | undefined,
    colourSeq = 0;
  $effect(() => {
    // Lines before the window give the lexer its context (open comments,
    // strings); lines after let a construct find its end.
    const from = Math.max(0, first - 150),
      to = Math.min(rows.length, last + 20);
    const missing = rows.slice(first, last).some((r) => r && !colours.has(r));
    if (!missing || loading) return;
    const slice = rows.slice(from, to);
    clearTimeout(colourTimer);
    colourTimer = setTimeout(async () => {
      const mine = ++colourSeq;
      try {
        const result = await api<{ lines: string[] }>("/api/highlight", {
          method: "POST",
          body: JSON.stringify({ path, text: slice.join("\n") }),
        });
        if (mine !== colourSeq) return;
        if (colours.size > 20000) colours.clear();
        slice.forEach((r, i) => r && colours.set(r, result.lines[i] ?? escape(r)));
        painted++;
      } catch {}
    }, 60);
    return () => clearTimeout(colourTimer);
  });

  function sync() {
    if (!area) return;
    scrollTop = area.scrollTop;
    scrollLeft = area.scrollLeft;
    if (gutter) gutter.style.transform = `translateY(${-scrollTop}px)`;
  }

  // Insert through the browser's editing pipeline so undo keeps working.
  function insert(value: string, start: number, end: number) {
    if (!area) return;
    area.setSelectionRange(start, end);
    if (!document.execCommand("insertText", false, value)) {
      area.setRangeText(value, start, end, "end");
      text = area.value;
    }
  }

  function key(e: KeyboardEvent) {
    if (!area) return;
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s") {
      e.preventDefault();
      void save(false);
      return;
    }
    if (e.key === "Escape" && !dirty) {
      e.preventDefault();
      ondone(everSaved);
      return;
    }
    const { selectionStart: s, selectionEnd: f } = area;
    if (e.key === "Tab") {
      e.preventDefault();
      const lineStart = text.lastIndexOf("\n", s - 1) + 1;
      const block = text.slice(lineStart, f);
      if (!e.shiftKey && s === f) {
        insert(unit, s, f);
        return;
      }
      const rows = block.split("\n");
      const changed = rows
        .map((r) =>
          e.shiftKey
            ? r.startsWith(unit)
              ? r.slice(unit.length)
              : r.replace(/^[ \t]{1,4}/, "")
            : unit + r,
        )
        .join("\n");
      insert(changed, lineStart, f);
      area.setSelectionRange(lineStart, lineStart + changed.length);
      return;
    }
    if (e.key === "Enter" && !e.isComposing && !e.altKey && !e.metaKey && !e.ctrlKey) {
      e.preventDefault();
      const lineStart = text.lastIndexOf("\n", s - 1) + 1;
      const current = text.slice(lineStart, s);
      let indent = current.match(/^[ \t]*/)![0];
      if (/[{[(:]\s*$/.test(current)) indent += unit;
      insert("\n" + indent, s, f);
    }
  }

  async function save(overwrite: boolean) {
    if (saving || (!dirty && !overwrite)) return;
    saving = true;
    try {
      let expected = hash;
      if (overwrite) {
        const latest = await api<{ hash: string }>(
          "/api/file?path=" + encodeURIComponent(path),
        );
        expected = latest.hash;
      }
      const result = await api<{ hash: string; content: string }>("/api/file", {
        method: "PUT",
        body: JSON.stringify({ path, content: text, expected_hash: expected }),
      });
      hash = result.hash;
      original = text;
      rememberContent(path, { hash: result.hash, content: text });
      conflict = false;
      everSaved = true;
      savedAt = Date.now();
      forgetDefinitions();
    } catch (e) {
      if (e instanceof APIError && e.status === 409) conflict = true;
      else onerror((e as Error).message);
    } finally {
      saving = false;
    }
  }

  function revert() {
    if (!area) return;
    insert(original, 0, text.length);
    area.setSelectionRange(0, 0);
    caret();
  }

  function finish() {
    if (!dirty) return ondone(everSaved);
    void confirmDiscard(path).then((ok) => ok && ondone(everSaved));
  }

  function beforeunload(e: BeforeUnloadEvent) {
    if (dirty) e.preventDefault();
  }

  onMount(() => {
    void load();
  });
</script>

<svelte:window onbeforeunload={beforeunload} />
<div class="editor" in:fade={{ duration: 140 }}>
  <div class="code-toolbar editor-toolbar">
    <div>
      <FileCode2 size={14} /><span class="editing-badge">Editing</span><span
        class="subtle">{lines.toLocaleString()} lines · {unit === "\t" ? "tabs" : unit.length + " spaces"}</span
      >
      {#if dirty}<span class="dirty-dot" in:fade title="Unsaved changes"></span><span
          class="small">Unsaved</span
        >{:else if reloadedAt > savedAt}{#key reloadedAt}<span class="saved-flash" in:fly={{ y: 4 }}
            ><RefreshCw size={12} />Reloaded — changed on disk</span
          >{/key}{:else if savedAt}{#key savedAt}<span class="saved-flash" in:fly={{ y: 4 }}
            ><Check size={12} />Saved</span
          >{/key}{/if}
    </div>
    <div>
      <button
        class="button small-button"
        disabled={!dirty || saving}
        onclick={revert}
        title="Revert to the loaded version"><Undo2 size={13} />Revert</button
      ><button
        class="button small-button primary"
        disabled={!dirty || saving}
        onclick={() => save(false)}
        aria-label="Save file"
        ><Save size={13} />{saving ? "Saving…" : "Save"}<kbd>⌘S</kbd></button
      ><button class="button small-button" onclick={finish} aria-label="Done editing"
        ><X size={13} />Done</button
      >
    </div>
  </div>
  {#if conflict}<div class="editor-conflict" in:fly={{ y: -6 }}>
      <TriangleAlert size={15} />
      <span
        >This file changed on disk since you opened it. Your edits are still
        here; nothing was overwritten.</span
      >
      <button class="button small-button" onclick={load}
        ><RefreshCw size={12} />Load theirs</button
      ><button
        class="button small-button"
        onclick={() => navigator.clipboard?.writeText(text)}
        ><Copy size={12} />Copy mine</button
      ><button class="button small-button primary" onclick={() => save(true)}
        >Overwrite with mine</button
      >
    </div>{/if}
  {#if loading}<div class="skeleton-stack editor-loading">
      {#each [0, 1, 2] as i}<div class="skeleton" style:--i={i}></div>{/each}
    </div>{:else if loadError}<div class="empty-state">
      <TriangleAlert size={26} />
      <h3>This file can’t be edited here</h3>
      <p>{loadError}</p>
      <button class="button" onclick={() => ondone(false)}>Back to viewer</button>
    </div>{:else}<div class="editor-body">
      <pre class="editor-gutter" aria-hidden="true" bind:this={gutter}>{numbers}</pre>
      <div class="editor-stack" bind:clientHeight={viewHeight}>
      <div
        class="editor-highlight"
        aria-hidden="true"
        style:transform="translate({-scrollLeft}px, {-scrollTop}px)"
      >
        <div class="editor-current" style:top="{(line - 1) * LH}px"></div>
        <div style:height="{first * LH}px"></div>
        {#each visible as html}<div class="code-text">{@html html || " "}</div>{/each}
      </div>
      <textarea
        bind:this={area}
        bind:value={text}
        class="editor-text"
        aria-label={`Edit ${path}`}
        spellcheck="false"
        autocapitalize="off"
        autocomplete="off"
        wrap="off"
        onkeydown={key}
        onkeyup={caret}
        onclick={caret}
        onselect={caret}
        onscroll={sync}
        oninput={caret}
      ></textarea>
      </div>
    </div>{/if}
  <div class="code-foot">
    <span>Ln {line}, Col {column}</span><span
      >{dirty ? "Modified" : "Unmodified"} · hash-checked save to the working tree</span
    >
  </div>
</div>
