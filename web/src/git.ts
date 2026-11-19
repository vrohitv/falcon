export interface GitFile {
  path: string;
  old_path?: string;
  index: string;
  worktree: string;
  conflict: boolean;
  protected: boolean;
}
export interface GitBranch {
  name: string;
  head: string;
  current: boolean;
  upstream: string;
  worktree: string;
  managed: boolean;
}
export interface GitState {
  branch: string;
  head: string;
  revision: string;
  index_hash: string;
  clean: boolean;
  detached: boolean;
  operation: string;
  files: GitFile[];
  branches: GitBranch[];
  author_name: string;
  author_email: string;
  upstream: string;
  ahead: number;
  behind: number;
  head_pushed: boolean;
  remotes: { name: string; url: string }[];
  remote_branches: string[];
  stashes: { ref: string; message: string; when: string }[];
  last_message: string;
  merge_message?: string;
}
export interface GitCommit {
  hash: string;
  parents: string[];
  author: string;
  email: string;
  date: string;
  subject: string;
  message: string;
}
export interface GitLog {
  head: string;
  commits: GitCommit[];
  next: number;
  backend: string;
}
export interface GitDetail {
  commit: GitCommit;
  files: GitFile[];
}
export interface GitResult {
  id: string;
  request: { id: string; action: string };
  status: "succeeded" | "failed";
  result?: string;
  error?: string;
  // The repository right after the action, and the change event it caused.
  state?: GitState;
  seq?: number;
}
export interface DiffRow {
  type: string;
  hunk?: number;
  oldLine?: number;
  newLine?: number;
  text: string;
  html?: string;
}
export interface DiffData {
  path: string;
  hunks: {
    oldStart: number;
    newStart: number;
    section?: string;
    rows: DiffRow[];
  }[];
  binary: boolean;
  truncated: boolean;
  metadata_only: boolean;
  hunk_ids?: string[];
}
