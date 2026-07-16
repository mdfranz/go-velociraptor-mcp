package raptor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireProcessLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raptor-mcp.lock")

	first, err := AcquireProcessLock(path)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	if _, err := AcquireProcessLock(path); err == nil {
		t.Fatal("expected second lock acquisition to fail")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}

	second, err := AcquireProcessLock(path)
	if err != nil {
		t.Fatalf("reacquire lock: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("release second lock: %v", err)
	}
}

func TestAcquireProcessLockRemovesInvalidStaleLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raptor-mcp.lock")
	if err := os.WriteFile(path, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	lock, err := AcquireProcessLock(path)
	if err != nil {
		t.Fatalf("replace stale lock: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("release lock: %v", err)
	}
}

func TestAcquireProcessLockOff(t *testing.T) {
	lock, err := AcquireProcessLock("off")
	if err != nil {
		t.Fatal(err)
	}
	if lock != nil {
		t.Fatal("expected no lock when disabled")
	}
}
