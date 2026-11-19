// Motion helpers. CSS animations are disabled by a reduced-motion media rule;
// these wrappers do the same for Svelte's JavaScript-driven transitions.
import {
  fade as svelteFade,
  fly as svelteFly,
  scale as svelteScale,
} from "svelte/transition";
import { cubicOut, backOut } from "svelte/easing";

function prefersReducedMotion() {
  return (
    typeof matchMedia === "function" &&
    matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

type Params = { delay?: number; duration?: number; [key: string]: unknown };
const calm = (p: Params = {}) =>
  prefersReducedMotion() ? { ...p, duration: 0, delay: 0 } : p;

export const fade = (node: Element, p: Params = {}) =>
  svelteFade(node, calm({ duration: 160, ...p }));
export const fly = (node: Element, p: Params = {}) =>
  svelteFly(node, calm({ duration: 260, y: 8, easing: cubicOut, ...p }));
export const pop = (node: Element, p: Params = {}) =>
  svelteScale(node, calm({ duration: 220, start: 0.94, easing: backOut, ...p }));
export const slide = (node: Element, p: Params = {}) =>
  svelteFly(
    node,
    calm({ duration: 300, x: 40, y: 0, opacity: 0, easing: cubicOut, ...p }),
  );
