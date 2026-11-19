// Lanes for a commit graph. Commits arrive newest first in topological order (each before its parents).
// Every row says where each line enters from above, where the commit's dot sits, and where lines leave
// below, so a row can be drawn on its own.

export type GraphCommit = { hash: string; parents: string[] };
export type Edge = { from: number; to: number; lane: number };
export type GraphRow = { lane: number; up: Edge[]; down: Edge[]; width: number };

export function layout(commits: GraphCommit[]): GraphRow[] {
  // lanes[i] is the commit a line in lane i is heading for (null: free).
  let lanes: (string | null)[] = [];
  const rows: GraphRow[] = [];
  // Commits already drawn: git's quick order puts a parent before its child only when their clocks
  // disagree, and then no line waits for it (it would run to the bottom of the list).
  const seen = new Set<string>();
  for (const c of commits) {
    seen.add(c.hash);
    let lane = lanes.indexOf(c.hash);
    if (lane < 0) {
      lane = lanes.indexOf(null);
      if (lane < 0) lane = lanes.length;
      lanes[lane] = c.hash;
    }
    const up: Edge[] = [];
    lanes.forEach((h, i) => {
      if (h === null) return;
      // Lines heading here end at the dot; the others pass straight through.
      up.push({ from: i, to: h === c.hash ? lane : i, lane: i });
    });
    const next = lanes.map((h) => (h === c.hash ? null : h));
    const down: Edge[] = [];
    c.parents.forEach((p, k) => {
      if (seen.has(p)) return;
      let target = next.indexOf(p);
      if (k === 0 && target > lane) {
        // The first parent is already awaited further right: bring that line over into this lane, so
        // the main line stays leftmost.
        next[target] = null;
        next[lane] = p;
        down.push({ from: target, to: lane, lane: target });
        target = lane;
      } else if (target < 0) {
        if (k === 0) target = lane;
        else {
          target = next.indexOf(null);
          if (target < 0 || target === lane) {
            const free = next.findIndex((h, i) => h === null && i !== lane);
            target = free >= 0 ? free : next.length;
          }
        }
        next[target] = p;
      }
      down.push({ from: lane, to: target, lane: k === 0 ? lane : target });
    });
    next.forEach((h, i) => {
      if (h !== null && !c.parents.includes(h)) down.push({ from: i, to: i, lane: i });
    });
    while (next.length && next[next.length - 1] === null) next.pop();
    rows.push({ lane, up, down, width: Math.max(lanes.length, next.length, lane + 1) });
    lanes = next;
  }
  return rows;
}
