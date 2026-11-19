// Keep modal keyboard focus inside the active dialog and return it on close.
export function dialogFocus(node: HTMLElement) {
  const previous = document.activeElement as HTMLElement | null;
  const elements = () =>
    Array.from(
      node.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), a[href], [tabindex="0"]',
      ),
    ).filter((el) => el.offsetParent !== null);
  queueMicrotask(() => elements()[0]?.focus());
  function onKey(event: KeyboardEvent) {
    if (event.key === "Tab") {
      const focusable = elements();
      if (!focusable.length) {
        event.preventDefault();
        node.focus();
        return;
      }
      const first = focusable[0],
        last = focusable.at(-1)!;
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }
  }
  node.addEventListener("keydown", onKey);
  return {
    destroy() {
      node.removeEventListener("keydown", onKey);
      previous?.focus();
    },
  };
}
