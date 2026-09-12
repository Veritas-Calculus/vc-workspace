package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Veritas-Calculus/vc-workspace/internal/gateway"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := gateway.LoadRuntimeConfig()
	if err != nil {
		logger.Error("session Gateway configuration is invalid")
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := gateway.RunService(ctx, cfg, logger); err != nil {
		logger.Error("session Gateway stopped", "error", err)
		os.Exit(1)
	}
}
