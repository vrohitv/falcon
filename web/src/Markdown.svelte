<script lang="ts">
  import { renderMarkdown } from "./markdown";
  // Markdown from a language server's hover. The renderer escapes all text, so {@html} only ever receives
  // its own fixed set of tags; web links open in a new tab.
  let { source, compact = false }: { source: string; compact?: boolean } = $props();
  const html = $derived(renderMarkdown(source || ""));
</script>

<div class="markdown" class:compact>
  {@html html}
</div>
