<script lang="ts">
  // The diff view: inline or side by side, syntax highlighted, with the characters that changed inside a
  // line marked. In Source Control a hunk can be staged, unstaged or discarded, and so can chosen lines
  // (click a line number; shift-click for a range). "Whole file" shows every line around the changes.
  import { onDestroy, tick } from "svelte";
  import type { DiffData, DiffRow } from "./git";
  import { Columns2, Rows3, ChevronUp, ChevronDown, FoldVertical, UnfoldVertical } from "@lucide/svelte";
  let {
    data,
    area = "",
    busy = false,
    full = false,
    onfull,
    onhunk,
    onlines,
  }: {
    data: DiffData;
    /** working or staged: hunks and lines can be acted on. */
    area?: string;
    busy?: boolean;
    full?: boolean;
    onfull?: (full: boolean) => void;
    onhunk?: (action: "hunk.stage" | "hunk.unstage" | "hunk.discard", id: string) => void;
    onlines?: (action: "lines.stage" | "lines.unstage" | "lines.discard", id: string, lines: number[]) => void;
  } = $props();

  let split = $state(false);
  try {
    split = localStorage.getItem("falcon.diff.split") === "1";
  } catch {}
  function setSplit(v: boolean) {
    split = v;
    try {
      localStorage.setItem("falcon.diff.split", v ? "1" : "0");
    } catch {}
  }
  const ids = $derived(data.hunk_ids || []);
  const actionable = $derived(!!onhunk && (area === "working" || area === "staged") && !full);
  const LIMIT = 8000;
  const total = $derived((data.hunks || []).reduce((n, h) => n + h.rows.length, 0));
  // Whole hunks up to the limit, so a hunk's index still matches its ID.
  const hunks = $derived.by(() => {
    const out = [];
    let n = 0;
    for (const h of data.hunks || []) {
      if (n && n + h.rows.length > LIMIT) break;
      out.push(h);
      n += h.rows.length;
    }
    return out;
  });

  // Rows paired for the side-by-side view: context on both sides, a run of removals next to the run of
  // additions that replaced it.
  type Pair = { left?: DiffRow & { i: number }; right?: DiffRow & { i: number } };
  function pairs(rows: DiffRow[]): Pair[] {
    const out: Pair[] = [];
    let i = 0;
    while (i < rows.length) {
      const r = rows[i];
      if (r.type === "ctx") {
        out.push({ left: { ...r, i }, right: { ...r, i } });
        i++;
        continue;
      }
      const dels: (DiffRow & { i: number })[] = [],
        adds: (DiffRow & { i: number })[] = [];
      while (i < rows.length && rows[i].type === "del") dels.push({ ...rows[i], i: i++ });
      while (i < rows.length && rows[i].type === "add") adds.push({ ...rows[i], i: i++ });
      for (let k = 0; k < Math.max(dels.length, adds.length); k++) out.push({ left: dels[k], right: adds[k] });
    }
    return out;
  }

  // Character-level changes: each removed line is compared with the added line that replaced it, word by
  // word; the parts that differ are marked with the CSS Highlight API (no change to the highlighted HTML).
  function tokens(s: string) {
    return s.match(/\w+|\s+|[^\w\s]/g) || [];
  }
  function changed(a: string, b: string): [[number, number][], [number, number][]] {
    const x = tokens(a),
      y = tokens(b);
    if (x.length * y.length > 40000) return [[[0, a.length]], [[0, b.length]]];
    const dp: number[][] = Array.from({ length: x.length + 1 }, () => new Array(y.length + 1).fill(0));
    for (let i = x.length - 1; i >= 0; i--)
      for (let j = y.length - 1; j >= 0; j--) dp[i][j] = x[i] === y[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    const ra: [number, number][] = [],
      rb: [number, number][] = [];
    let i = 0,
      j = 0,
      pa = 0,
      pb = 0;
    const mark = (list: [number, number][], from: number, to: number) => {
      if (to <= from) return;
      const last = list.at(-1);
      if (last && last[1] === from) last[1] = to;
      else list.push([from, to]);
    };
    while (i < x.length || j < y.length) {
      if (i < x.length && j < y.length && x[i] === y[j]) {
        pa += x[i++].length;
        pb += y[j++].length;
      } else if (j < y.length && (i >= x.length || dp[i][j + 1] >= dp[i + 1][j])) {
        mark(rb, pb, pb + y[j].length);
        pb += y[j++].length;
      } else {
        mark(ra, pa, pa + x[i].length);
        pa += x[i++].length;
      }
    }
    // A line that changed completely is already marked by its colour.
    const whole = (r: [number, number][], s: string) => r.length === 1 && r[0][0] === 0 && r[0][1] >= s.trimEnd().length;
    return [whole(ra, a) ? [] : ra, whole(rb, b) ? [] : rb];
  }
  let root = $state<HTMLElement>();
  const supported = typeof CSS !== "undefined" && "highlights" in CSS;
  function rangesIn(code: Element, spans: [number, number][]) {
    const out: Range[] = [];
    const walker = document.createTreeWalker(code, NodeFilter.SHOW_TEXT);
    const nodes: { n: Text; at: number }[] = [];
    let at = 0,
      n: Node | null;
    while ((n = walker.nextNode())) {
      nodes.push({ n: n as Text, at });
      at += n.textContent!.length;
    }
    for (const [a, b] of spans) {
      const r = document.createRange();
      const s = nodes.findLast((x) => x.at <= a),
        e = nodes.findLast((x) => x.at < b);
      if (!s || !e) continue;
      try {
        r.setStart(s.n, a - s.at);
        r.setEnd(e.n, b - e.at);
        out.push(r);
      } catch {}
    }
    return out;
  }
  async function markWords() {
    if (!supported || !root) return;
    await tick();
    const dels: Range[] = [],
      adds: Range[] = [];
    hunks.forEach((h, hi) => {
      for (const p of pairs(h.rows)) {
        if (!p.left || !p.right || p.left.type !== "del" || p.right.type !== "add") continue;
        const [ra, rb] = changed(p.left.text, p.right.text);
        const lc = root!.querySelector(`[data-h="${hi}"][data-r="${p.left.i}"] code`),
          rc = root!.querySelector(`[data-h="${hi}"][data-r="${p.right.i}"] code`);
        if (lc && ra.length) dels.push(...rangesIn(lc, ra));
        if (rc && rb.length) adds.push(...rangesIn(rc, rb));
      }
    });
    const reg = (CSS as any).highlights;
    reg.set("diff-word-del", new (window as any).Highlight(...dels));
    reg.set("diff-word-add", new (window as any).Highlight(...adds));
  }
  $effect(() => {
    void data;
    void split;
    void markWords();
  });
  onDestroy(() => {
    if (!supported) return;
    (CSS as any).highlights.delete("diff-word-del");
    (CSS as any).highlights.delete("diff-word-add");
  });

  // Choosing lines: within one hunk, added and removed lines only.
  let chosen = $state<{ hunk: number; rows: Set<number>; last: number } | null>(null);
  $effect(() => {
    void data;
    chosen = null;
  });
  function choose(hunk: number, row: number, e: MouseEvent) {
    if (!actionable || !onlines) return;
    const type = hunks[hunk].rows[row].type;
    if (type !== "add" && type !== "del") return;
    if (!chosen || chosen.hunk !== hunk) chosen = { hunk, rows: new Set(), last: row };
    const rows = new Set(chosen.rows);
    if (e.shiftKey) {
      const [a, b] = [Math.min(chosen.last, row), Math.max(chosen.last, row)];
      for (let i = a; i <= b; i++) if (hunks[hunk].rows[i].type !== "ctx") rows.add(i);
    } else if (rows.has(row)) rows.delete(row);
    else rows.add(row);
    chosen = rows.size ? { hunk, rows, last: row } : null;
  }
  function act(action: "lines.stage" | "lines.unstage" | "lines.discard") {
    if (!chosen || !onlines) return;
    onlines(action, ids[chosen.hunk], [...chosen.rows].sort((a, b) => a - b));
  }
  const isChosen = (h: number, r: number) => !!chosen && chosen.hunk === h && chosen.rows.has(r);

  // Hunk to hunk: the buttons, or ] and [ when the diff has focus.
  let current = $state(0);
  function go(step: number) {
    if (!hunks.length || !root) return;
    current = (current + step + hunks.length) % hunks.length;
    root.querySelector(`[data-hunk="${current}"]`)?.scrollIntoView({ block: "start", behavior: "smooth" });
  }
  const sign = (t: string) => (t === "add" ? "+" : t === "del" ? "−" : " ");
</script>

{#snippet code(row: DiffRow | undefined)}{#if row}{#if row.html !== undefined}{@html row.html}{:else}{row.text}{/if}{/if}{/snippet}

<div class="diff" class:split>
  <div class="diff-bar">
    <span class="diff-modes" role="group" aria-label="Diff layout">
      <button class:on={!split} aria-pressed={!split} title="Inline" onclick={() => setSplit(false)}><Rows3 size={13} />Inline</button><button
        class:on={split}
        aria-pressed={split}
        title="Side by side"
        onclick={() => setSplit(true)}><Columns2 size={13} />Side by side</button
      >
    </span>
    {#if onfull}<button class="diff-chip" class:on={full} aria-pressed={full} title={full ? "Show only the changes" : "Show the whole file"} onclick={() => onfull?.(!full)}
        >{#if full}<FoldVertical size={13} />Changes only{:else}<UnfoldVertical size={13} />Whole file{/if}</button
      >{/if}
    <span class="diff-gap"></span>
    {#if chosen && actionable}<span class="diff-chosen"
        >{chosen.rows.size} line{chosen.rows.size === 1 ? "" : "s"}
        {#if area === "staged"}<button disabled={busy} onclick={() => act("lines.unstage")}>Unstage lines</button>{:else}<button disabled={busy} onclick={() => act("lines.discard")}>Discard lines</button><button
            class="primary"
            disabled={busy}
            onclick={() => act("lines.stage")}>Stage lines</button
          >{/if}<button class="diff-clear" onclick={() => (chosen = null)} aria-label="Clear the chosen lines">×</button></span
      >{:else if actionable && hunks.length}<span class="diff-hint">Click line numbers to choose lines</span>{/if}
    {#if hunks.length > 1}<span class="diff-nav"
        ><button class="icon-button" title="Previous change ([)" aria-label="Previous change" onclick={() => go(-1)}><ChevronUp size={14} /></button><span>{current + 1}/{hunks.length}</span><button
          class="icon-button"
          title="Next change (])"
          aria-label="Next change"
          onclick={() => go(1)}><ChevronDown size={14} /></button
        ></span
      >{/if}
  </div>
  <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
  <div
    class="diff-body"
    bind:this={root}
    tabindex="0"
    role="region"
    aria-label="File diff"
    onkeydown={(e) => {
      if (e.key === "]") go(1);
      if (e.key === "[") go(-1);
      if (e.key === "Escape") chosen = null;
    }}
  >
    {#if data.binary}<div class="git-diff-message">Binary file changed. Text preview is unavailable.</div>
    {:else if !hunks.length}<div class="git-diff-message">
        {data.metadata_only ? "File metadata or filename changed; no text changes." : "No changes in this comparison."}
      </div>
    {:else}
      {#if data.truncated}<div class="git-diff-message">Preview limited to 512 KiB.</div>{/if}
      {#if total > LIMIT}<div class="git-diff-message">A very large diff: the first {LIMIT.toLocaleString()} lines are shown.</div>{/if}
      {#each hunks as h, hi}
        <div class="diff-hunk" data-hunk={hi}>
          <span class="diff-hunk-label">@@ −{h.oldStart} +{h.newStart} @@ <em>{h.section || ""}</em></span>
          {#if actionable && ids[hi]}<span class="git-hunk-actions"
              >{#if area === "staged"}<button disabled={busy} onclick={() => onhunk?.("hunk.unstage", ids[hi])}>Unstage hunk</button
                >{:else}<button disabled={busy} onclick={() => onhunk?.("hunk.discard", ids[hi])}>Discard hunk</button><button
                  disabled={busy}
                  class="primary"
                  onclick={() => onhunk?.("hunk.stage", ids[hi])}>Stage hunk</button
                >{/if}</span
            >{/if}
        </div>
        {#if split}
          {#each pairs(h.rows) as p}<div class="diff-pair">
              <div class="diff-side {p.left?.type || 'empty'}" class:chosen={p.left && p.left.type !== "ctx" && isChosen(hi, p.left.i)} data-h={hi} data-r={p.left?.i}>
                <button class="diff-ln" tabindex="-1" disabled={!p.left || p.left.type === "ctx" || !actionable} onclick={(e) => p.left && choose(hi, p.left.i, e)}>{p.left?.oldLine || ""}</button><span
                  class="diff-sign">{p.left ? sign(p.left.type) : ""}</span
                ><code>{@render code(p.left)}</code>
              </div>
              <div class="diff-side {p.right?.type || 'empty'}" class:chosen={p.right && p.right.type !== "ctx" && isChosen(hi, p.right.i)} data-h={p.right && p.right.type !== "ctx" ? hi : undefined} data-r={p.right && p.right.type !== "ctx" ? p.right.i : undefined}>
                <button class="diff-ln" tabindex="-1" disabled={!p.right || p.right.type === "ctx" || !actionable} onclick={(e) => p.right && choose(hi, p.right.i, e)}>{p.right?.newLine || ""}</button><span
                  class="diff-sign">{p.right ? sign(p.right.type) : ""}</span
                ><code>{@render code(p.right)}</code>
              </div>
            </div>{/each}
        {:else}
          {#each h.rows as row, ri}<div class="diff-row {row.type}" class:chosen={isChosen(hi, ri)} data-h={hi} data-r={ri}>
              <button class="diff-ln" tabindex="-1" disabled={row.type === "ctx" || !actionable} onclick={(e) => choose(hi, ri, e)}
                ><span>{row.oldLine || ""}</span><span>{row.newLine || ""}</span></button
              ><span class="diff-sign">{sign(row.type)}</span><code>{@render code(row)}</code>
            </div>{/each}
        {/if}
      {/each}
    {/if}
  </div>
</div>
