// Package server implements the demo-api HTTP service: business endpoint,
// health endpoint, Prometheus metrics and optional fault injection used to
// simulate a bad release during canary analysis.
package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config holds the runtime configuration of the service.
type Config struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string
	// Version is reported in responses and in the build_info metric.
	Version string
	// ErrorRate is the fraction (0..1) of requests to "/" answered with HTTP 500.
	ErrorRate float64
	// Latency is extra latency added to every request to "/".
	Latency time.Duration
}

// Defaults are the build-time defaults (set through -ldflags) that
// environment variables may override.
type Defaults struct {
	Version   string
	ErrorRate string
	LatencyMs string
}

// Environment variables understood by the service.
const (
	EnvPort           = "PORT"
	EnvFaultErrorRate = "FAULT_ERROR_RATE"
	EnvFaultLatencyMs = "FAULT_LATENCY_MS"
)

// ConfigFromEnv builds a Config from build-time defaults overridden by
// environment variables read through getenv.
func ConfigFromEnv(getenv func(string) string, d Defaults) (Config, error) {
	cfg := Config{Addr: ":8080", Version: d.Version}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}

	if port := strings.TrimSpace(getenv(EnvPort)); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return Config{}, fmt.Errorf("invalid %s %q", EnvPort, port)
		}
		cfg.Addr = ":" + port
	}

	rate, err := parseRate(pick(getenv(EnvFaultErrorRate), d.ErrorRate))
	if err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", EnvFaultErrorRate, err)
	}
	cfg.ErrorRate = rate

	latency, err := parseLatency(pick(getenv(EnvFaultLatencyMs), d.LatencyMs))
	if err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", EnvFaultLatencyMs, err)
	}
	cfg.Latency = latency

	return cfg, nil
}

func pick(override, fallback string) string {
	if v := strings.TrimSpace(override); v != "" {
		return v
	}
	return strings.TrimSpace(fallback)
}

func parseRate(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if v < 0 || v > 1 {
		return 0, errors.New("must be between 0 and 1")
	}
	return v, nil
}

func parseLatency(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	ms, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if ms < 0 || ms > 60000 {
		return 0, errors.New("must be between 0 and 60000")
	}
	return time.Duration(ms) * time.Millisecond, nil
}
