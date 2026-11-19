// In-app confirmation, instead of window.confirm: a native dialog blocks the
// page (and every automation driving it) and cannot match the app's look.
type Request = {
  title: string;
  message: string;
  confirm: string;
  cancel: string;
  danger: boolean;
  resolve: (ok: boolean) => void;
};
let current = $state<{ request: Request | null }>({ request: null });
export const confirmation = current;

export function confirmDialog(
  message: string,
  options: { title?: string; confirm?: string; cancel?: string; danger?: boolean } = {},
): Promise<boolean> {
  current.request?.resolve(false);
  return new Promise((resolve) => {
    current.request = {
      title: options.title ?? "Are you sure?",
      message,
      confirm: options.confirm ?? "Continue",
      cancel: options.cancel ?? "Cancel",
      danger: options.danger ?? false,
      resolve: (ok) => {
        current.request = null;
        resolve(ok);
      },
    };
  });
}

/** Ask before discarding unsaved edits; resolves true to discard. */
export function confirmDiscard(what: string) {
  return confirmDialog(`Your unsaved changes to ${what} will be lost.`, {
    title: "Discard unsaved changes?",
    confirm: "Discard changes",
    cancel: "Keep editing",
    danger: true,
  });
}
