//go:build !windows

package treewatch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/fsnotify/fsnotify"
)

// maxDirs bounds the directories watched. On Linux each is one inotify watch (cheap; the system's limit
// is what usually stops it). Elsewhere (kqueue) every file costs an open descriptor, so only small trees.
func maxDirs() int {
	if runtime.GOOS == "linux" {
		return 500_000
	}
	return 2_000
}

var errTooLarge = errors.New("too many directories to watch")

// start watches every directory of the tree (but .git and ignored ones), and each new one as it appears.
func (w *Watcher) start() (func(), error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	count := 0
	add := func(dir string) error {
		return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == dir {
					return err
				}
				return nil // vanished meanwhile
			}
			if !d.IsDir() {
				return nil
			}
			if rel := w.rel(p); p != w.root && (w.skipped(rel) || w.ignore(rel)) {
				return filepath.SkipDir
			}
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			if count++; count > maxDirs() {
				return errTooLarge
			}
			return fw.Add(p)
		})
	}
	if err := add(w.root); err != nil {
		fw.Close()
		return nil, err
	}
	w.done.Add(1)
	go func() {
		defer w.done.Done()
		for {
			select {
			case <-w.stop:
				return
			case e, ok := <-fw.Events:
				if !ok {
					return
				}
				rel := w.rel(e.Name)
				switch {
				case e.Op&fsnotify.Create != 0:
					// A new directory is watched, and whatever was created in it before the watch
					// began is covered by reporting the directory itself.
					if info, err := os.Lstat(e.Name); err == nil && info.IsDir() && !w.skipped(rel) && !w.ignore(rel) {
						if err := add(e.Name); errors.Is(err, errTooLarge) {
							w.lost()
						}
					}
					w.note(rel, true)
				case e.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
					w.note(rel, true)
				case e.Op&fsnotify.Write != 0:
					w.note(rel, false)
				}
			case err, ok := <-fw.Errors:
				if !ok {
					return
				}
				if errors.Is(err, fsnotify.ErrEventOverflow) {
					w.lost()
				}
			}
		}
	}()
	return func() { fw.Close() }, nil
}
