package main

import (
	"io"
	"log/slog"
	"testing"
)

func TestRunRejectsInvalidConfig(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := map[string]string{"FAULT_ERROR_RATE": "2"}
	if err := run(logger, func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error for out-of-range error rate")
	}
}

func TestRunFailsOnUnusablePort(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := map[string]string{"PORT": "notaport"}
	if err := run(logger, func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error for invalid port")
	}
}
