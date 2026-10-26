package lock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidName(t *testing.T) {
	for n, want := range map[string]bool{"db": true, "a.b-c_d": true, "": false, ".x": false, "a/b": false, "a b": false, `a\b`: false} {
		if ValidName(n) != want {
			t.Errorf("%q", n)
		}
	}
}

func TestExclusiveAndRelease(t *testing.T) {
	dir := t.TempDir()
	l, _, err := TryAcquire(dir, "x")
	if err != nil || l == nil {
		t.Fatal(err)
	}
	l2, holder, err := TryAcquire(dir, "x")
	if err != nil || l2 != nil || holder != os.Getpid() {
		t.Fatalf("second acquire: %v %v %d", l2, err, holder)
	}
	l.Release()
	l3, _, err := TryAcquire(dir, "x")
	if err != nil || l3 == nil {
		t.Fatal("not free after release", err)
	}
	l3.Release()
}

func TestStaleLockIsReclaimed(t *testing.T) {
	dir := t.TempDir()
	// A pid that cannot be alive.
	if err := os.WriteFile(filepath.Join(dir, "x.lock"), []byte("2147483646"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, _, err := TryAcquire(dir, "x")
	if err != nil || l == nil {
		t.Fatalf("stale lock not reclaimed: %v", err)
	}
	l.Release()
}

func TestAcquireStopsWhenAsked(t *testing.T) {
	dir := t.TempDir()
	held, _, _ := TryAcquire(dir, "x")
	defer held.Release()
	stop := make(chan struct{})
	go func() { time.Sleep(300 * time.Millisecond); close(stop) }()
	_, err := Acquire(dir, "x", nil, stop)
	if err != ErrStopped {
		t.Fatalf("want ErrStopped, got %v", err)
	}
}

func TestAcquireAny(t *testing.T) {
	dir := t.TempDir()
	a, _, _ := TryAcquire(dir, "s1")
	defer a.Release()
	l, err := AcquireAny(dir, []string{"s1", "s2"}, nil, make(chan struct{}))
	if err != nil || l.Name != "s2" {
		t.Fatalf("%v %+v", err, l)
	}
	l.Release()
}
