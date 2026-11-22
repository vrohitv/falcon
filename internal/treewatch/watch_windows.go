//go:build windows

package treewatch

import (
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// start opens one recursive watch on the whole tree (ReadDirectoryChangesW with the subtree flag): a
// single handle however many directories there are. Changes under .git and ignored directories are
// dropped as they arrive.
func (w *Watcher) start() (func(), error) {
	name, err := windows.UTF16PtrFromString(w.root)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	ready, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	quit, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(ready)
		windows.CloseHandle(h)
		return nil, err
	}
	const mask = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME |
		windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE | windows.FILE_NOTIFY_CHANGE_CREATION
	// 64 KB: the most a network share accepts. DWORD-aligned, as the call requires.
	buf := make([]uint32, 16<<10)
	ov := &windows.Overlapped{HEvent: ready}
	read := func() error {
		return windows.ReadDirectoryChanges(h, (*byte)(unsafe.Pointer(&buf[0])), uint32(len(buf)*4), true, mask, nil, ov, 0)
	}
	if err := read(); err != nil {
		windows.CloseHandle(quit)
		windows.CloseHandle(ready)
		windows.CloseHandle(h)
		return nil, err
	}
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		defer windows.CloseHandle(ready)
		defer windows.CloseHandle(h)
		for {
			r, err := windows.WaitForMultipleObjects([]windows.Handle{ready, quit}, false, windows.INFINITE)
			if err != nil || r != windows.WAIT_OBJECT_0 {
				windows.CancelIoEx(h, ov)
				var n uint32
				_ = windows.GetOverlappedResult(h, ov, &n, true)
				return
			}
			var n uint32
			if err := windows.GetOverlappedResult(h, ov, &n, false); err != nil || n == 0 {
				// The buffer overflowed: the changes it could not hold are lost.
				w.lost()
			} else {
				w.parse((*[64 << 10]byte)(unsafe.Pointer(&buf[0]))[:n:n])
			}
			if err := read(); err != nil {
				w.lost()
				return
			}
		}
	}()
	return func() {
		windows.SetEvent(quit)
		reader.Wait()
		windows.CloseHandle(quit)
	}, nil
}

func (w *Watcher) parse(b []byte) {
	for off := 0; off+12 <= len(b); {
		info := (*windows.FileNotifyInformation)(unsafe.Pointer(&b[off]))
		n := int(info.FileNameLength / 2)
		name := windows.UTF16ToString(unsafe.Slice(&info.FileName, n))
		rel := filepath.ToSlash(name)
		switch info.Action {
		case windows.FILE_ACTION_MODIFIED:
			w.note(rel, false, false)
		case windows.FILE_ACTION_ADDED, windows.FILE_ACTION_RENAMED_NEW_NAME:
			w.note(rel, true, true)
		default: // removed, renamed away
			w.note(rel, true, false)
		}
		if info.NextEntryOffset == 0 {
			break
		}
		off += int(info.NextEntryOffset)
	}
}
