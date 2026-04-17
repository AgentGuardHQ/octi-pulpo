package dispatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrWorktreeRace is returned by repoLock callers when `git worktree add`
// fails after we held the sidecar flock — the signature of a stale
// `.git/config.lock` or an otherwise-contended repo.
//
// Adapters surface this via result.Status="failed" + result.Error prefixed
// with "worktree race:" so Sentinel stops needing hourly log forensics to
// spot the ganglia-sr silent-loss pattern.
var ErrWorktreeRace = errors.New("worktree race")

// staleConfigLockTTL is how old `<repoPath>/.git/config.lock` must be
// before repoLock will opportunistically remove it. Measured from after
// we already hold the sidecar flock, so we never race our own siblings.
const staleConfigLockTTL = 60 * time.Second

// repoLock acquires an exclusive lock on a sidecar file
// (`<repoPath>/.git/octi-worktree.lock`) so concurrent adapter dispatches
// against the same repo can't race each other inside `git worktree add`.
//
// Git serializes its own writes to `.git/config` via `.git/config.lock`,
// but `git worktree add -b <branch>` writes upstream tracking into the
// parent repo's config — two parallel calls with overlapping config writes
// occasionally lose that race and exit 255 with
// "could not lock config file .git/config: File exists". This helper
// serializes the `worktree add` prelude per-repo at the OS level via
// flock(2), which works across processes (systemd timers can fork multiple
// dispatcher procs — a sync.Mutex wouldn't catch that).
//
// Scope must stay tight: release() before any long-running subprocess
// starts. Holding the flock across the 10-min clawta run would serialize
// all dispatch per-repo and tank throughput.
//
// While holding the flock, repoLock also opportunistically removes
// `<repoPath>/.git/config.lock` if it is older than staleConfigLockTTL —
// the canonical "previous run crashed" footprint that would otherwise
// still block us even with our own serialization in place.
func repoLock(repoPath string) (release func(), err error) {
	gitDir := filepath.Join(repoPath, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		return nil, fmt.Errorf("repoLock: ensure .git dir: %w", err)
	}

	lockPath := filepath.Join(gitDir, "octi-worktree.lock")
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("repoLock: open %s: %w", lockPath, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("repoLock: flock %s: %w", lockPath, err)
	}

	// Opportunistic stale-lock removal: now that we hold the sidecar
	// flock, any .git/config.lock that outlived its creator is safe to
	// remove — no concurrent sibling can race us on creating a new one.
	removeStaleConfigLock(gitDir)

	release = func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return release, nil
}

// removeStaleConfigLock deletes `<gitDir>/config.lock` if it is older than
// staleConfigLockTTL. Best-effort; errors are swallowed because the caller
// is about to try `git worktree add` anyway — git will surface any real
// problem.
func removeStaleConfigLock(gitDir string) {
	configLock := filepath.Join(gitDir, "config.lock")
	info, err := os.Stat(configLock)
	if err != nil {
		return
	}
	if time.Since(info.ModTime()) < staleConfigLockTTL {
		return
	}
	_ = os.Remove(configLock)
}
