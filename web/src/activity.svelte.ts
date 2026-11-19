// How many requests to the server are running, so the mark can show Falcon is working.
let inflight = $state(0);
export const activity = {
  get inflight() {
    return inflight;
  },
  begin() {
    inflight++;
  },
  end() {
    inflight = Math.max(0, inflight - 1);
  },
};
(globalThis as any).__falconActivity = activity;
