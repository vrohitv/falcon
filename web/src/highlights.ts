// The browser's highlight registry (CSS Custom Highlight API) is one per page, by name: two code views
// that each set "lsp-error" would replace each other's. Each view sets its own ranges here, and the
// registry gets every view's ranges for a name together.
const supported = typeof CSS !== "undefined" && "highlights" in CSS;
const byName = new Map<string, Map<object, Range[]>>();

function publish(name: string) {
  const owners = byName.get(name);
  const all = owners ? [...owners.values()].flat() : [];
  const registry = (CSS as any).highlights;
  if (all.length) registry.set(name, new (window as any).Highlight(...all));
  else registry.delete(name);
}

/** Sets one view's ranges for a highlight name (none clears them). */
export function setHighlight(owner: object, name: string, ranges: Range[]) {
  if (!supported) return;
  let owners = byName.get(name);
  if (!owners) byName.set(name, (owners = new Map()));
  if (ranges.length) owners.set(owner, ranges);
  else if (!owners.delete(owner)) return;
  publish(name);
}

/** Clears everything a view set (it is going away). */
export function clearHighlights(owner: object) {
  if (!supported) return;
  for (const [name, owners] of byName) if (owners.delete(owner)) publish(name);
}
