<script lang="ts">
  // The call hierarchy of a function (Shift+Alt+H), as in VS Code: who calls it, and who calls those, a
  // level at a time as each is opened; or the other way, what it calls. Each entry opens where the call
  // is made (callers) or where the function is (callees).
  import { untrack } from "svelte";
  import { ChevronRight, ArrowDownLeft, ArrowUpRight } from "@lucide/svelte";
  import { callsOf, type Call, type CallItem } from "./definition";

  let {
    root,
    path,
    onopen,
    onclose,
  }: {
    root: CallItem;
    /** The file the hierarchy was asked from (its language server answers). */
    path: string;
    onopen: (path: string, line: number) => void;
    onclose: () => void;
  } = $props();

  type Node = { key: string; call: Call | null; item: CallItem; depth: number; open: boolean; loading: boolean; children: Node[] | null; error: string };
  let direction = $state<"incoming" | "outgoing">("incoming");
  // The tree is plain objects; version moves whenever one changes (opened, loaded), redrawing the rows.
  let top: Node | null = null;
  let version = $state(0);
  let counter = 0;

  function node(item: CallItem, call: Call | null, depth: number): Node {
    return { key: String(counter++), call, item, depth, open: false, loading: false, children: null, error: "" };
  }
  async function expand(n: Node) {
    if (n.open) {
      n.open = false;
      version++;
      return;
    }
    n.open = true;
    if (!n.children) {
      n.loading = true;
      version++;
      try {
        const calls = await callsOf(path, n.item, direction);
        n.children = calls.map((c) => node(c.item, c, n.depth + 1));
      } catch (e) {
        n.error = (e as Error).message;
        n.children = [];
      } finally {
        n.loading = false;
      }
    }
    version++;
  }
  // A new tree whenever the direction changes (and only then: the rest is read untracked).
  $effect(() => {
    void direction;
    untrack(() => {
      const t = node(root, null, 0);
      top = t;
      version++;
      void expand(t);
    });
  });
  function flat(n: Node | null, out: Node[] = []): Node[] {
    if (!n) return out;
    out.push(n);
    if (n.open && n.children) for (const c of n.children) flat(c, out);
    return out;
  }
  const rows = $derived.by(() => {
    void version;
    return flat(top);
  });
  const kinds: Record<number, string> = { 5: "class", 6: "method", 9: "constructor", 12: "function", 23: "struct", 11: "interface" };
  function place(n: Node) {
    // A caller opens at the call; a callee (and the root) at its declaration.
    if (n.call && direction === "incoming" && n.call.lines.length) return n.call.lines[0];
    return n.item.line;
  }
</script>

<div class="calls">
  <div class="panel-heading">
    <h3>{direction === "incoming" ? "Callers of" : "Calls from"} {root.name}</h3>
    <div class="calls-switch" role="tablist">
      <button role="tab" aria-selected={direction === "incoming"} class:on={direction === "incoming"} onclick={() => (direction = "incoming")}
        ><ArrowDownLeft size={13} />Incoming</button
      ><button role="tab" aria-selected={direction === "outgoing"} class:on={direction === "outgoing"} onclick={() => (direction = "outgoing")}
        ><ArrowUpRight size={13} />Outgoing</button
      >
    </div>
    <button class="button small-button" onclick={onclose}>Back to source</button>
  </div>
  <div class="calls-tree" role="tree">
    {#each rows as n (n.key)}<div class="call-row" role="treeitem" aria-selected="false" aria-expanded={n.open} style:padding-left="{8 + n.depth * 16}px">
        <button class="call-twist" class:open={n.open} aria-label={n.open ? "Collapse" : "Expand"} onclick={() => expand(n)}><ChevronRight size={13} /></button>
        <button class="call-name" onclick={() => onopen(n.item.path, place(n))} ondblclick={() => expand(n)}>
          <span class="symbol-kind">{(kinds[n.item.kind] || "fn").slice(0, 2)}</span><b>{n.item.name}</b>
          {#if n.item.detail}<span class="call-detail">{n.item.detail}</span>{/if}
          <span class="call-where">{n.item.path.replace(/^~ext\/[^/]+\//, "")}:{place(n)}{#if n.call && n.call.lines.length > 1} · {n.call.lines.length} calls{/if}</span>
        </button>
      </div>
      {#if n.open && n.loading}<div class="call-note" style:padding-left="{40 + n.depth * 16}px">Looking…</div>{/if}
      {#if n.open && n.children && !n.children.length && !n.loading}<div class="call-note" style:padding-left="{40 + n.depth * 16}px">
          {n.error || (direction === "incoming" ? "No callers" : "No calls")}
        </div>{/if}{/each}
  </div>
</div>

<style>
  .calls {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
  .calls-switch {
    display: flex;
    margin-left: auto;
    border: 1px solid var(--line);
    border-radius: 4px;
    overflow: hidden;
  }
  .calls-switch button {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 3px 8px;
    border: 0;
    background: none;
    color: var(--muted);
    font-size: 11px;
    cursor: pointer;
  }
  .calls-switch button.on {
    background: var(--surface-hover);
    color: var(--bright);
  }
  .calls-tree {
    overflow: auto;
    padding: 4px 0;
    font-size: 12px;
  }
  .call-row {
    display: flex;
    align-items: center;
    min-height: 24px;
  }
  .call-row:hover {
    background: var(--hover-wash);
  }
  .call-twist {
    display: grid;
    place-items: center;
    width: 20px;
    height: 20px;
    padding: 0;
    border: 0;
    background: none;
    color: var(--subtle);
    cursor: pointer;
    transition: transform 0.12s var(--ease-out);
  }
  .call-twist.open {
    transform: rotate(90deg);
  }
  .call-name {
    display: flex;
    flex: 1;
    align-items: baseline;
    gap: 7px;
    min-width: 0;
    padding: 2px 8px 2px 2px;
    border: 0;
    background: none;
    color: var(--text);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .call-name b {
    color: var(--bright);
    font-weight: 600;
  }
  .call-detail {
    overflow: hidden;
    color: var(--muted);
    font-family: var(--font-mono);
    font-size: 11px;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .call-where {
    margin-left: auto;
    color: var(--subtle);
    font-size: 11px;
    white-space: nowrap;
  }
  .call-note {
    padding: 3px 0;
    color: var(--subtle);
    font-size: 11px;
  }
</style>
