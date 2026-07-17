package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mdfranz/go-velociraptor-mcp/internal/raptor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	flagConfig := ""
	for i, arg := range os.Args[1:] {
		if arg == "--config" && i+1 < len(os.Args[1:]) {
			flagConfig = os.Args[i+2]
		}
	}

	cfg, err := raptor.LoadConfig(flagConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}

	logCloser, err := raptor.InitLogger(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logger init error:", err)
		return 1
	}
	if logCloser != nil {
		defer logCloser.Close()
	}

	lockCloser, err := raptor.AcquireProcessLock(cfg.LockFile)
	if err != nil {
		slog.Error("lock acquisition failed", "error", err, "lock_file", cfg.LockFile)
		fmt.Fprintln(os.Stderr, "lock error:", err)
		return 1
	}
	if lockCloser != nil {
		defer lockCloser.Close()
	}

	slog.Info("server starting",
		"version", version,
		"api_connection_string", cfg.APIConnectionString,
		"pinned_server_name", cfg.PinnedServerName,
		"org_id", cfg.OrgID,
		"max_response_bytes", cfg.MaxResponseBytes,
		"timeout", cfg.DefaultTimeout.String(),
		"disabled_tools", cfg.DisabledTools,
		"log_file", cfg.LogFile,
		"lock_file", cfg.LockFile,
		"data_dir", cfg.DataDir,
	)

	client, err := raptor.NewClient(cfg)
	if err != nil {
		slog.Error("client init failed", "error", err)
		fmt.Fprintln(os.Stderr, "connect error:", err)
		return 1
	}
	defer client.Close()

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "raptor-mcp",
		Version: version,
	}, nil)

	registerTools(srv, client, cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Fprintln(os.Stderr, "raptor-mcp", version, "starting on stdio")
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && err != context.Canceled {
		slog.Error("server stopped with error", "error", err)
		fmt.Fprintln(os.Stderr, "server error:", err)
		return 1
	}
	slog.Info("server stopped")
	return 0
}
