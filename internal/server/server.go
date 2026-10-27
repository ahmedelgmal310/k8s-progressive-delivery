package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// latencyBuckets are tuned around the 300 ms p95 SLO used by the canary analysis.
var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.3, 0.5, 1, 2.5}

// Metrics groups the collectors exported by the service.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewMetrics registers the service collectors on reg.
func NewMetrics(reg prometheus.Registerer, version string) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests processed, partitioned by route, method and status code.",
		}, []string{"route", "method", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency, partitioned by route and status code.",
			Buckets: latencyBuckets,
		}, []string{"route", "code"}),
	}
	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "demo_api_build_info",
		Help:        "Build information of the running demo-api binary.",
		ConstLabels: prometheus.Labels{"version": version},
	})
	buildInfo.Set(1)
	reg.MustRegister(m.requests, m.duration, buildInfo)
	return m
}

// Server serves the demo-api endpoints.
type Server struct {
	cfg      Config
	metrics  *Metrics
	gatherer prometheus.Gatherer
	hostname string
	logger   *slog.Logger

	// random returns a value in [0,1); replaceable in tests.
	random func() float64
	// sleep waits for d or until ctx is done; replaceable in tests.
	sleep func(ctx context.Context, d time.Duration)
}

// New returns a Server with its own Prometheus registry.
func New(cfg Config, logger *slog.Logger) *Server {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return &Server{
		cfg:      cfg,
		metrics:  NewMetrics(reg, cfg.Version),
		gatherer: reg,
		hostname: host,
		logger:   logger,
		// Fault injection only needs a uniform distribution, not a CSPRNG.
		random: rand.Float64, //nolint:gosec // non-security use: fault injection sampling
		sleep:  sleepContext,
	}
}

// Handler returns the HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", s.instrument("/", http.HandlerFunc(s.handleRoot)))
	mux.Handle("GET /healthz", s.instrument("/healthz", http.HandlerFunc(handleHealth)))
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.gatherer, promhttp.HandlerOpts{}))
	return mux
}

type rootResponse struct {
	Service string `json:"service"`
	Version string `json:"version"`
	Pod     string `json:"pod"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Latency > 0 {
		s.sleep(r.Context(), s.cfg.Latency)
	}
	resp := rootResponse{Service: "demo-api", Version: s.cfg.Version, Pod: s.hostname}
	code := http.StatusOK
	if s.cfg.ErrorRate > 0 && s.random() < s.cfg.ErrorRate {
		code = http.StatusInternalServerError
		resp.Error = "injected fault"
	}
	writeJSON(w, code, resp)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// instrument records request count and latency for route.
func (s *Server) instrument(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		code := strconv.Itoa(rec.code)
		s.metrics.requests.WithLabelValues(route, r.Method, code).Inc()
		s.metrics.duration.WithLabelValues(route, code).Observe(time.Since(start).Seconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code        int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.code = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func sleepContext(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// Serve runs the HTTP server on ln until ctx is cancelled, then shuts down
// gracefully within shutdownTimeout.
func (s *Server) Serve(ctx context.Context, ln net.Listener, shutdownTimeout time.Duration) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      70 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	s.logger.Info("listening",
		"addr", ln.Addr().String(),
		"version", s.cfg.Version,
		"fault_error_rate", s.cfg.ErrorRate,
		"fault_latency", s.cfg.Latency.String(),
	)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	s.logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Run listens on cfg.Addr and serves until ctx is cancelled.
func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return err
	}
	return New(cfg, logger).Serve(ctx, ln, 10*time.Second)
}
