package repository

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/go-git/go-git/v5/plumbing/format/index"
)

// Ignorer answers what git leaves out of the folder, in memory, as git reads it: the .gitignore of every
// directory (deeper ones win), .git/info/exclude and the owner's global excludes file; and the files git
// tracks, which belong to the folder even when a rule matches them. No git process is started: the rule
// files and git's index are read directly, and read again only when they change (Sync).
type Ignorer struct {
	r  *Repository
	mu sync.Mutex
	// base: the global excludes file and info/exclude (the lowest precedence).
	base []gitignore.Pattern
	// own: each directory's own .gitignore ("" is the root), read on first need; stack: the rules in
	// force in a directory (base, then the root's, then each one down to it).
	own   map[string][]gitignore.Pattern
	stack map[string][]gitignore.Pattern
	// skip: directories not walked (ignored, and holding nothing tracked).
	skip        map[string]bool
	tracked     map[string]struct{}
	trackedDirs map[string]struct{}
	stamps      map[string]string // rule and index files' size and time, when last read
}

// Ignorer is the folder's ignore rules (made once).
func (r *Repository) Ignorer() *Ignorer {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ignorer == nil {
		r.ignorer = &Ignorer{r: r}
		r.ignorer.Sync()
	}
	return r.ignorer
}

// GitDirs are the repository's common git folder and this worktree's own (the same, outside linked
// worktrees), read from the folder: .git is the folder, or a file naming it; a linked worktree's own
// folder names the common one in "commondir".
func (r *Repository) GitDirs() (common, own string) {
	own = filepath.Join(r.Path, ".git")
	if b, err := os.ReadFile(own); err == nil {
		if line, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:"); ok {
			own = strings.TrimSpace(line)
			if !filepath.IsAbs(own) {
				own = filepath.Join(r.Path, own)
			}
		}
	}
	common = own
	if b, err := os.ReadFile(filepath.Join(own, "commondir")); err == nil {
		common = strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(own, common)
		}
	}
	return filepath.Clean(common), filepath.Clean(own)
}

func stamp(name string) string {
	info, err := os.Stat(name)
	if err != nil {
		return ""
	}
	return fmt.Sprint(info.ModTime().UnixNano(), "|", info.Size())
}

// globalExcludes is the owner's excludes file: core.excludesFile from their git configuration, or git's
// default ($XDG_CONFIG_HOME/git/ignore, ~/.config/git/ignore).
func (ig *Ignorer) globalExcludes() string {
	home, _ := os.UserHomeDir()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" && home != "" {
		xdg = filepath.Join(home, ".config")
	}
	common, _ := ig.r.GitDirs()
	name := ""
	for _, cfg := range []string{filepath.Join(xdg, "git", "config"), filepath.Join(home, ".gitconfig"), filepath.Join(common, "config")} {
		if v := configValue(cfg, "core", "excludesfile"); v != "" {
			name = v // later files win, as in git
		}
	}
	if name == "" {
		return filepath.Join(xdg, "git", "ignore")
	}
	if rest, ok := strings.CutPrefix(name, "~/"); ok && home != "" {
		name = filepath.Join(home, rest)
	}
	return name
}

// configValue reads one key from a git configuration file (enough for core.excludesFile).
func configValue(file, section, key string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	in, out := false, ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			name := strings.ToLower(strings.TrimSpace(strings.Trim(line, "[]")))
			in = name == section
			continue
		}
		if !in {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), key) {
			out = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return out
}

func readPatterns(file string, domain []string) []gitignore.Pattern {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	var ps []gitignore.Pattern
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		s := sc.Text()
		if !strings.HasPrefix(s, "#") && strings.TrimSpace(s) != "" {
			ps = append(ps, gitignore.ParsePattern(s, domain))
		}
	}
	return ps
}

// Sync reads the rules and git's index again if any of their files changed (a few stats); call it before
// a walk or a batch of questions. A changed .gitignore inside the folder is reported with Reload.
func (ig *Ignorer) Sync() {
	common, own := ig.r.GitDirs()
	global := ig.globalExcludes()
	exclude := filepath.Join(common, "info", "exclude")
	idx := filepath.Join(own, "index")
	now := map[string]string{global: stamp(global), exclude: stamp(exclude), idx: stamp(idx)}
	ig.mu.Lock()
	defer ig.mu.Unlock()
	rules := ig.stamps == nil || ig.stamps[global] != now[global] || ig.stamps[exclude] != now[exclude]
	tracked := ig.stamps == nil || ig.stamps[idx] != now[idx]
	ig.stamps = now
	if rules {
		ig.base = append(readPatterns(global, nil), readPatterns(exclude, nil)...)
		ig.resetLocked()
	}
	if tracked {
		ig.tracked, ig.trackedDirs = map[string]struct{}{}, map[string]struct{}{}
		if f, err := os.Open(idx); err == nil {
			var x index.Index
			if index.NewDecoder(bufio.NewReader(f)).Decode(&x) == nil {
				for _, e := range x.Entries {
					ig.tracked[e.Name] = struct{}{}
					for d := path.Dir(e.Name); d != "." && d != "/"; d = path.Dir(d) {
						if _, ok := ig.trackedDirs[d]; ok {
							break
						}
						ig.trackedDirs[d] = struct{}{}
					}
				}
			}
			f.Close()
		}
		ig.skip = map[string]bool{}
	}
}

// Reload forgets the .gitignore files read (one of them changed).
func (ig *Ignorer) Reload() {
	ig.mu.Lock()
	ig.resetLocked()
	ig.mu.Unlock()
}

func (ig *Ignorer) resetLocked() {
	ig.own, ig.stack, ig.skip = map[string][]gitignore.Pattern{}, map[string][]gitignore.Pattern{}, map[string]bool{}
}

// rules are the patterns in force for paths inside dir ("" is the root).
func (ig *Ignorer) rules(dir string) []gitignore.Pattern {
	if ps, ok := ig.stack[dir]; ok {
		return ps
	}
	var parent []gitignore.Pattern
	var domain []string
	if dir == "" {
		parent = ig.base
	} else {
		up := path.Dir(dir)
		if up == "." {
			up = ""
		}
		parent = ig.rules(up)
		domain = strings.Split(dir, "/")
	}
	own, ok := ig.own[dir]
	if !ok {
		own = readPatterns(filepath.Join(ig.r.Path, filepath.FromSlash(dir), ".gitignore"), domain)
		ig.own[dir] = own
	}
	ps := parent
	if len(own) > 0 {
		ps = append(append(make([]gitignore.Pattern, 0, len(parent)+len(own)), parent...), own...)
	}
	ig.stack[dir] = ps
	return ps
}

func (ig *Ignorer) matchLocked(rel string, isDir bool) bool {
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	ps := ig.rules(dir)
	if len(ps) == 0 {
		return false
	}
	return gitignore.NewMatcher(ps).Match(strings.Split(rel, "/"), isDir)
}

// skipLocked: a directory not walked (ignored by a rule, or below one that is, and holding nothing
// tracked). Git does not look inside an ignored directory, so nothing in it can be brought back.
func (ig *Ignorer) skipLocked(dir string) bool {
	if v, ok := ig.skip[dir]; ok {
		return v
	}
	v := false
	if up := path.Dir(dir); up != "." && ig.skipLocked(up) {
		v = true
	} else if _, holds := ig.trackedDirs[dir]; !holds {
		v = ig.matchLocked(dir, true)
	}
	ig.skip[dir] = v
	return v
}

// SkipDir reports whether a folder-relative directory is left out (not walked, not watched).
func (ig *Ignorer) SkipDir(dir string) bool {
	if dir == "" || dir == "." {
		return false
	}
	ig.mu.Lock()
	defer ig.mu.Unlock()
	return ig.skipLocked(dir)
}

// Ignored reports whether git leaves a folder-relative path out, as git check-ignore does: a tracked
// file never is, nor a directory holding tracked files; otherwise a rule matches it or a directory above
// it is left out.
func (ig *Ignorer) Ignored(rel string, isDir bool) bool {
	ig.mu.Lock()
	defer ig.mu.Unlock()
	if isDir {
		return ig.skipLocked(rel)
	}
	if _, ok := ig.tracked[rel]; ok {
		return false
	}
	if up := path.Dir(rel); up != "." && ig.skipLocked(up) {
		return true
	}
	return ig.matchLocked(rel, false)
}
