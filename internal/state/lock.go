package state

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockWait caps how long a writer waits for the lock. The critical section is a
// small read + write, so anything near this means a stuck holder, not contention.
const lockWait = 2 * time.Second

// lockPath is a sidecar, not the store itself: Save replaces the store by
// rename, so a lock held on that inode would stop guarding the file that
// replaced it.
func lockPath() string { return Path() + ".lock" }

// Mutate loads the store, applies fn, and saves when fn reports a change, all
// under one exclusive lock. The lock has to span load-through-save: Save is
// atomic per writer, but two processes that each load, mutate their own copy
// and save will silently drop the first writer's rows. norn writes this file
// from the TUI and from per-tool-call activity ticks at the same time, so that
// interleaving is the normal case, not a corner one.
//
// The returned store is the state after fn ran, so a caller can keep reading it
// without a second load.
func Mutate(fn func(*Store) bool) (*Store, error) {
	unlock, err := lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	s, err := Load()
	if err != nil {
		return nil, err
	}
	// s.repaired means Load rewrote a path or dropped a duplicate, so the repair
	// has to reach disk even when fn changes nothing.
	if fn(s) || s.repaired {
		if err := s.Save(); err != nil {
			return s, err
		}
	}
	return s, nil
}

// lock takes the exclusive sidecar lock, polling rather than blocking so a
// stuck holder surfaces as an error instead of freezing the TUI.
func lock() (func(), error) {
	p := lockPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if err != syscall.EWOULDBLOCK {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", p, err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("lock %s: busy after %s", p, lockWait)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
