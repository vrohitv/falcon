// node --experimental-strip-types unit/graph.test.ts
import { layout } from "../src/graph.ts";
const assert = (ok: boolean, msg: string) => {
  if (!ok) throw new Error(msg);
};
// main: m2 ← merge(m1, s1); s1 ← base; m1 ← base
const rows = layout([
  { hash: "merge", parents: ["m1", "s1"] },
  { hash: "s1", parents: ["base"] },
  { hash: "m1", parents: ["base"] },
  { hash: "base", parents: [] },
]);
assert(rows[0].lane === 0 && rows[0].down.length === 2, "merge has two parents");
assert(rows[1].lane === 1, "the side branch gets its own lane");
assert(rows[2].lane === 0, "main stays in lane 0");
assert(rows[2].down.some((e) => e.from === 1 && e.to === 0), "the side line bends into the main lane");
assert(rows[3].lane === 0 && rows[3].width === 1, "base sits in lane 0 alone");
// A straight history stays in one lane.
const line = layout([{ hash: "c", parents: ["b"] }, { hash: "b", parents: ["a"] }, { hash: "a", parents: [] }]);
assert(line.every((r) => r.lane === 0 && r.width === 1), "linear history in one lane");
console.log("graph layout ok");
// Clock skew: git's quick order can list a parent before its child; no lane may wait for it forever.
const skew = layout([
  { hash: "a", parents: ["b"] },
  { hash: "b", parents: ["c"] },
  { hash: "x", parents: ["b"] },
  { hash: "c", parents: [] },
]);
assert(skew[2].down.every((e) => e.lane === 0 || e.from === e.to), "no new lane for an already drawn parent");
assert(Math.max(...skew.map((r) => r.width)) <= 2, "the graph stays narrow");
assert(skew[3].up.length === 1, "only the line to c remains above it");
console.log("graph layout: ok");
