// A tab being dragged, shared by every editor group: the browser does not let a drop target read a
// drag's data until the drop, and the groups need to know a tab is on its way to show where it would land.
export const tabDrag = $state<{ group: string; path: string } | null>({ group: "", path: "" });

export function startTabDrag(group: string, path: string) {
  tabDrag!.group = group;
  tabDrag!.path = path;
}
export function endTabDrag() {
  tabDrag!.group = "";
  tabDrag!.path = "";
}
export const dragging = () => !!tabDrag?.path;
