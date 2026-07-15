package raptor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxLogSize = 10 * 1024 * 1024 // 10 MiB cap

type rotatingFile struct {
	path string
	mu   sync.Mutex
	f    *os.File
	size int64
}

func openRotating(path string) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	info, _ := f.Stat()
	return &rotatingFile{path: path, f: f, size: info.Size()}, nil
}

func (l *rotatingFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size+int64(len(p)) > maxLogSize {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

func (l *rotatingFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		return l.f.Close()
	}
	return nil
}

func (l *rotatingFile) rotate() error {
	l.f.Close()
	backup := l.path + ".1"
	_ = os.Remove(backup)
	if err := os.Rename(l.path, backup); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	l.f, l.size = f, 0
	return nil
}

func parseLogLevel(lvl string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(lvl)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelDebug // Default to debug as requested
	}
}

func InitLogger(cfg *Config) (io.Closer, error) {
	level := parseLogLevel(cfg.LogLevel)

	if strings.EqualFold(cfg.LogFile, "off") {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level})))
		return nil, nil
	}

	logFile := cfg.LogFile
	if logFile == "" {
		logFile = "raptor-mcp.log"
	}

	lf, err := openRotating(logFile)
	if err != nil {
		return nil, err
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(lf, &slog.HandlerOptions{Level: level})))
	return lf, nil
}

type slogWriter struct {
	level slog.Level
	buf   strings.Builder
}

func NewSlogWriter(level slog.Level) *slogWriter {
	return &slogWriter{level: level}
}

func (w *slogWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.buf.Write(p)
	for {
		s := w.buf.String()
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			break
		}
		slog.Log(context.Background(), w.level, strings.TrimSpace(s[:idx]))
		w.buf.Reset()
		w.buf.WriteString(s[idx+1:])
	}
	return n, nil
}
