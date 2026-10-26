// Package lock provides advisory resource locks that work the same on every operating system.
//
// A lock is a file created exclusively (O_EXCL is atomic on every platform) that holds its owner's
// process id. A lock whose owner is gone is reclaimed, so a killed process cannot wedge the queue.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sarx/internal/proc"
)

// ErrStopped is returned when waiting for a lock is abandoned.
var ErrStopped = errors.New("stopped while waiting for lock")

// emptyGrace is how long a lock file with no pid yet is given before it is suspected.
const emptyGrace = 2 * time.Second

// Lock is a held lock.
type Lock struct {
	path string
	Name string
}

// ValidName reports whether name is usable as a lock name.
func ValidName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		ok := r == '.' || r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return false
		}
	}
	return !strings.HasPrefix(name, ".")
}

// TryAcquire takes the lock if it is free. It returns the holder's pid (0 if unknown) when it is not.
func TryAcquire(dir, name string) (*Lock, int, error) {
	if !ValidName(name) {
		return nil, 0, fmt.Errorf("lock name %q may only contain letters, digits, dot, dash and underscore", name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, 0, err
	}
	path := filepath.Join(dir, name+".lock")
	for attempt := 0; attempt < 3; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, werr := f.WriteString(strconv.Itoa(os.Getpid()))
			f.Close()
			if werr != nil {
				os.Remove(path)
				return nil, 0, werr
			}
			return &Lock{path: path, Name: name}, 0, nil
		}
		if !os.IsExist(err) {
			return nil, 0, err
		}
		holder, stale := inspect(path)
		if !stale {
			return nil, holder, nil
		}
		// Claim the stale file by renaming it away: only one process can win the rename.
		grave := fmt.Sprintf("%s.stale-%d-%d", path, os.Getpid(), time.Now().UnixNano())
		if os.Rename(path, grave) == nil {
			os.Remove(grave)
		}
	}
	return nil, 0, nil
}

// inspect reads a lock file and reports its holder and whether the lock is stale.
func inspect(path string) (holder int, stale bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		// The file vanished between the failed create and now; try again.
		return 0, true
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		st, err := os.Stat(path)
		if err != nil {
			return 0, true
		}
		return 0, time.Since(st.ModTime()) > emptyGrace
	}
	pid, err := strconv.Atoi(s)
	if err != nil {
		return 0, true
	}
	return pid, !proc.Alive(pid)
}

// Acquire waits for the lock. wait is called once, with the holder's pid, if the lock is busy.
// Closing stop abandons the wait.
func Acquire(dir, name string, wait func(holder int), stop <-chan struct{}) (*Lock, error) {
	return AcquireAny(dir, []string{name}, wait, stop)
}

// AcquireAny waits until any one of the named locks is free and takes it.
func AcquireAny(dir string, names []string, wait func(holder int), stop <-chan struct{}) (*Lock, error) {
	told := false
	for {
		var holder int
		for _, n := range names {
			l, h, err := TryAcquire(dir, n)
			if err != nil {
				return nil, err
			}
			if l != nil {
				return l, nil
			}
			holder = h
		}
		if !told && wait != nil {
			wait(holder)
			told = true
		}
		select {
		case <-stop:
			return nil, ErrStopped
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// Release frees the lock. Releasing twice is harmless.
func (l *Lock) Release() {
	if l == nil || l.path == "" {
		return
	}
	os.Remove(l.path)
	l.path = ""
}
