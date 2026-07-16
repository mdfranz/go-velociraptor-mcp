package raptor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type processLock struct {
	path string
	pid  string
	file *os.File
	once sync.Once
	err  error
}

func AcquireProcessLock(path string) (io.Closer, error) {
	if strings.EqualFold(strings.TrimSpace(path), "off") {
		return nil, nil
	}
	if path == "" {
		return nil, fmt.Errorf("lock file path is required")
	}

	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create lock directory: %w", err)
		}
	}

	pid := strconv.Itoa(os.Getpid())
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, err := file.WriteString(pid + "\n"); err != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("write lock file: %w", err)
			}
			if err := file.Sync(); err != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("sync lock file: %w", err)
			}
			return &processLock{path: path, pid: pid, file: file}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create lock file: %w", err)
		}

		ownerPID, active := lockOwner(path)
		if active {
			return nil, fmt.Errorf("another raptor-mcp process is running with pid %d", ownerPID)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale lock file: %w", err)
		}
	}
	return nil, fmt.Errorf("could not acquire lock file %s", path)
}

func lockOwner(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return pid, false
	}
	err = process.Signal(syscall.Signal(0))
	return pid, err == nil || !errors.Is(err, syscall.ESRCH)
}

func (l *processLock) Close() error {
	l.once.Do(func() {
		if l.file != nil {
			l.err = l.file.Close()
		}
		data, err := os.ReadFile(l.path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) && l.err == nil {
				l.err = err
			}
			return
		}
		if strings.TrimSpace(string(data)) != l.pid {
			return
		}
		if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) && l.err == nil {
			l.err = err
		}
	})
	return l.err
}
