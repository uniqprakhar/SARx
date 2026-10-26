// Package util holds small helpers that have no dependencies on the rest of sarx.
package util

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// UUID returns a random version 4 UUID in lower case.
func UUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not recoverable in any useful way; fall back to the clock.
		n := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(n >> (uint(i) * 3))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// WriteFileAtomic writes data to a temporary file in the same directory and renames it over path,
// so a reader never sees a half-written file. On Windows a rename can fail briefly while another
// process has the target open, so it retries a few times.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	_ = os.Chmod(tmpName, perm)
	var last error
	attempts := 1
	if runtime.GOOS == "windows" {
		attempts = 10
	}
	for i := 0; i < attempts; i++ {
		if last = os.Rename(tmpName, path); last == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	os.Remove(tmpName)
	return last
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns free text into a short file-name-safe word group.
func Slug(s string, max int) string {
	s = slugRe.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-")
	if len(s) > max {
		s = strings.Trim(s[:max], "-")
	}
	if s == "" {
		s = "task"
	}
	return s
}

// Age renders a number of seconds as 12s, 3m or 1h05m.
func Age(secs int64) string {
	if secs < 0 {
		secs = 0
	}
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 3600:
		return fmt.Sprintf("%dm", secs/60)
	default:
		return fmt.Sprintf("%dh%02dm", secs/3600, (secs%3600)/60)
	}
}

// Now is the current time in unix seconds.
func Now() int64 { return time.Now().Unix() }

// Tail returns the last n lines of s.
func Tail(s string, n int) string {
	s = strings.TrimRight(s, "\r\n")
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Exists reports whether a path exists.
func Exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// IsTerminal reports whether f is an interactive terminal.
func IsTerminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// ToSlash normalises a path to forward slashes for matching and display in files.
func ToSlash(p string) string { return filepath.ToSlash(p) }
