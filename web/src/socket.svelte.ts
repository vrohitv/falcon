import { apiURL } from "./api";

// The page's one WebSocket to its folder: git changes, changes to open files, and terminals all arrive
// on it (separate event streams used to exhaust the browser's six connections per host, stalling a third
// Falcon tab). It reconnects by itself, re-sending what it was asked to watch; terminals re-attach.
type Message = { t: string; [key: string]: any };
type Listener = (m: Message) => void;
type BinaryListener = (data: Uint8Array) => void;

export const socketState = $state({ open: false, terminal: false });

let ws: WebSocket | null = null,
  repo = "",
  retry = 0,
  timer: ReturnType<typeof setTimeout> | undefined,
  watched: string[] = [];
const listeners = new Map<string, Set<Listener>>();
const binary = new Map<string, BinaryListener>();
const openers = new Set<() => void>();
const outbox: (string | Uint8Array<ArrayBuffer>)[] = [];
const encoder = new TextEncoder();

/** Connects to a folder (once per page; switching folders reconnects). */
export function connect(repoID: string) {
  if (repo === repoID && ws) return;
  repo = repoID;
  retry = 0;
  open();
}

function open() {
  clearTimeout(timer);
  if (ws) {
    ws.onclose = null;
    ws.close();
  }
  if (!repo) return;
  const url = new URL(apiURL("/api/ws"), location.href);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  const sock = new WebSocket(url);
  sock.binaryType = "arraybuffer";
  ws = sock;
  sock.onopen = () => {
    retry = 0;
    socketState.open = true;
    if (watched.length) sock.send(JSON.stringify({ t: "watch", paths: watched }));
    for (const m of outbox.splice(0)) sock.send(m);
    for (const f of openers) f();
  };
  sock.onmessage = (e) => {
    if (typeof e.data === "string") {
      let m: Message;
      try {
        m = JSON.parse(e.data);
      } catch {
        return;
      }
      if (m.t === "hello") socketState.terminal = !!m.terminal;
      for (const f of listeners.get(m.t) ?? []) f(m);
      return;
    }
    const data = new Uint8Array(e.data as ArrayBuffer);
    const n = data[0];
    const id = new TextDecoder().decode(data.subarray(1, 1 + n));
    binary.get(id)?.(data.subarray(1 + n));
  };
  sock.onclose = () => {
    socketState.open = false;
    if (ws !== sock) return;
    ws = null;
    // Back off: 0.3 s, 0.6 s, … up to 5 s.
    timer = setTimeout(open, Math.min(5000, 300 * 2 ** retry++));
  };
}

/** Sends a message now, or when the connection is (re)established. */
export function send(m: Message | Uint8Array<ArrayBuffer>) {
  const data = m instanceof Uint8Array ? m : JSON.stringify(m);
  if (ws?.readyState === WebSocket.OPEN) ws.send(data);
  else outbox.push(data);
}

/** Typed input for a terminal session. */
export function sendInput(id: string, text: string) {
  const idBytes = encoder.encode(id);
  const body = encoder.encode(text);
  const frame = new Uint8Array(1 + idBytes.length + body.length);
  frame[0] = idBytes.length;
  frame.set(idBytes, 1);
  frame.set(body, 1 + idBytes.length);
  // Typing while reconnecting would land in the wrong order; it is dropped (as a terminal would).
  if (ws?.readyState === WebSocket.OPEN) ws.send(frame);
}

/** Listens for one type of message; returns the way to stop. */
export function on(type: string, f: Listener) {
  let set = listeners.get(type);
  if (!set) listeners.set(type, (set = new Set()));
  set.add(f);
  return () => set!.delete(f);
}

/** Receives a terminal session's output. */
export function onOutput(id: string, f: BinaryListener) {
  binary.set(id, f);
  return () => {
    if (binary.get(id) === f) binary.delete(id);
  };
}

/** Runs f each time the connection opens (to attach again after a reconnect). */
export function onOpen(f: () => void) {
  openers.add(f);
  if (ws?.readyState === WebSocket.OPEN) f();
  return () => openers.delete(f);
}

/** The files whose changes to push (the open tabs). */
export function watch(paths: string[]) {
  const next = [...new Set(paths)].sort();
  if (next.join("\0") === watched.join("\0")) return;
  watched = next;
  send({ t: "watch", paths: next });
}

/** Closes the connection (the page leaves the folder). */
export function disconnect() {
  repo = "";
  watched = [];
  clearTimeout(timer);
  if (ws) {
    ws.onclose = null;
    ws.close();
    ws = null;
  }
  socketState.open = false;
}
