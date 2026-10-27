// Command demo-api is a small HTTP service used to demonstrate progressive
// delivery with Argo Rollouts. A "bad" build can be produced by setting the
// fault defaults at build time, e.g.:
//
//	go build -ldflags "-X main.defaultErrorRate=0.3" ./cmd/demo-api
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ahmedelgmal310/k8s-progressive-delivery/internal/server"
)

// Set through -ldflags at build time.
var (
	version          = "dev"
	defaultErrorRate = "0"
	defaultLatencyMs = "0"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger, os.Getenv); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, getenv func(string) string) error {
	cfg, err := server.ConfigFromEnv(getenv, server.Defaults{
		Version:   version,
		ErrorRate: defaultErrorRate,
		LatencyMs: defaultLatencyMs,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Run(ctx, cfg, logger)
}
