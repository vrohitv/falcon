export interface TreeFile {
  path: string;
  oldPath?: string;
  status?: string;
  description?: string;
  disabled?: boolean;
  directory?: boolean;
  loaded?: boolean;
  ignored?: boolean;
}
export interface TreeNode {
  id: string;
  path: string;
  name: string;
  parent: string;
  file?: TreeFile;
  directory?: TreeFile;
  children: TreeNode[];
  count: number;
  actionable: number;
}
export interface TreeRow {
  node: TreeNode;
  depth: number;
  position: number;
  siblings: number;
  expanded: boolean;
}
const collator = new Intl.Collator(undefined, {
  numeric: true,
  sensitivity: "base",
});

export function buildFileTree(files: TreeFile[], filter: string): TreeNode[] {
  const roots: TreeNode[] = [],
    folders = new Map<string, TreeNode>();
  const query = filter.trim().toLocaleLowerCase();
  for (const file of files) {
    if (query && !file.path.toLocaleLowerCase().includes(query)) continue;
    const parts = file.path.split("/");
    let children = roots,
      parent = "";
    for (let i = 0; i < parts.length - (file.directory ? 0 : 1); i++) {
      const path = parts.slice(0, i + 1).join("/");
      let folder = folders.get(path);
      if (!folder) {
        folder = {
          id: "folder:" + path,
          path,
          name: parts[i],
          parent,
          children: [],
          count: 0,
          actionable: 0,
        };
        folders.set(path, folder);
        children.push(folder);
      }
      if (!file.directory) {
        folder.count++;
        if (!file.disabled) folder.actionable++;
      } else if (i === parts.length - 1) folder.directory = file;
      parent = folder.id;
      children = folder.children;
    }
    if (!file.directory)
      children.push({
        id: "file:" + file.path,
        path: file.path,
        name: parts.at(-1)!,
        parent,
        file,
        children: [],
        count: 1,
        actionable: file.disabled ? 0 : 1,
      });
  }
  const sort = (nodes: TreeNode[]) => {
    nodes.sort(
      (a, b) =>
        Number(!!a.file) - Number(!!b.file) ||
        collator.compare(a.name, b.name) ||
        a.name.localeCompare(b.name),
    );
    for (const node of nodes) if (!node.file) sort(node.children);
  };
  sort(roots);
  return roots;
}

export function flattenTree(
  nodes: TreeNode[],
  expanded: Set<string>,
  searching: boolean,
): TreeRow[] {
  const rows: TreeRow[] = [];
  const visit = (siblings: TreeNode[], depth: number) => {
    siblings.forEach((node, i) => {
      const open = !node.file && (searching || expanded.has(node.path));
      rows.push({
        node,
        depth,
        position: i + 1,
        siblings: siblings.length,
        expanded: open,
      });
      if (open) visit(node.children, depth + 1);
    });
  };
  visit(nodes, 0);
  return rows;
}

export function actionablePaths(node: TreeNode): string[] {
  if (node.file) return node.file.disabled ? [] : [node.path];
  return node.children.flatMap(actionablePaths);
}
