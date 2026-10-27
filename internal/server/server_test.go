package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestServer(cfg Config) *Server {
	s := New(cfg, testLogger())
	s.random = func() float64 { return 0.5 }
	s.sleep = func(context.Context, time.Duration) {}
	return s
}

func do(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), method, path, nil))
	return rec
}

func TestRootHealthy(t *testing.T) {
	s := newTestServer(Config{Version: "v2"})
	rec := do(t, s.Handler(), http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body rootResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Version != "v2" || body.Service != "demo-api" || body.Error != "" {
		t.Fatalf("unexpected body %+v", body)
	}
	if got := testutil.ToFloat64(s.metrics.requests.WithLabelValues("/", "GET", "200")); got != 1 {
		t.Fatalf("requests_total{code=200} = %v, want 1", got)
	}
}

func TestRootInjectsErrors(t *testing.T) {
	s := newTestServer(Config{Version: "v3", ErrorRate: 0.6})
	h := s.Handler()

	s.random = func() float64 { return 0.1 } // below rate: fail
	if rec := do(t, h, http.MethodGet, "/"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	s.random = func() float64 { return 0.9 } // above rate: succeed
	if rec := do(t, h, http.MethodGet, "/"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := testutil.ToFloat64(s.metrics.requests.WithLabelValues("/", "GET", "500")); got != 1 {
		t.Fatalf("requests_total{code=500} = %v, want 1", got)
	}
}

func TestRootInjectsLatency(t *testing.T) {
	s := newTestServer(Config{Version: "v3", Latency: 400 * time.Millisecond})
	var slept time.Duration
	s.sleep = func(_ context.Context, d time.Duration) { slept = d }
	if rec := do(t, s.Handler(), http.MethodGet, "/"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if slept != 400*time.Millisecond {
		t.Fatalf("slept %v, want 400ms", slept)
	}
}

func TestHealthzAndMetrics(t *testing.T) {
	s := newTestServer(Config{Version: "v1", ErrorRate: 1})
	h := s.Handler()

	// Health stays green even for a faulty build: probes alone cannot catch it.
	if rec := do(t, h, http.MethodGet, "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}
	do(t, h, http.MethodGet, "/")

	rec := do(t, h, http.MethodGet, "/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`demo_api_build_info{version="v1"} 1`,
		`http_requests_total{code="500",method="GET",route="/"} 1`,
		`http_requests_total{code="200",method="GET",route="/healthz"} 1`,
		`http_request_duration_seconds_bucket{code="500",route="/",le="0.3"} 1`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q in:\n%s", want, body)
		}
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	h := newTestServer(Config{}).Handler()
	if rec := do(t, h, http.MethodGet, "/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestStatusRecorderKeepsFirstCode(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder(), code: http.StatusOK}
	rec.WriteHeader(http.StatusTeapot)
	rec.WriteHeader(http.StatusOK)
	if rec.code != http.StatusTeapot {
		t.Fatalf("code = %d, want 418", rec.code)
	}
}

func TestSleepContext(t *testing.T) {
	start := time.Now()
	sleepContext(context.Background(), 5*time.Millisecond)
	if time.Since(start) < 5*time.Millisecond {
		t.Fatal("returned before the delay elapsed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	sleepContext(ctx, time.Minute)
	if time.Since(start) > time.Second {
		t.Fatal("did not return on cancelled context")
	}
}

func TestNewMetricsRegisters(t *testing.T) {
	reg := prometheus.NewRegistry()
	NewMetrics(reg, "x")
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(mfs) == 0 {
		t.Fatal("no metrics gathered")
	}
}

func TestServeAndGracefulShutdown(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(Config{Version: "it"}, testLogger()).Serve(ctx, ln, 5*time.Second) }()

	url := "http://" + ln.Addr().String() + "/healthz"
	var resp *http.Response
	for range 50 {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		resp, err = http.DefaultClient.Do(req)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server never answered: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

func TestServeReturnsListenerError(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close() // Serve on a closed listener fails immediately
	if err := New(Config{}, testLogger()).Serve(context.Background(), ln, time.Second); err == nil {
		t.Fatal("expected error from closed listener")
	}
}

func TestRunListenError(t *testing.T) {
	if err := Run(context.Background(), Config{Addr: "256.0.0.1:bad"}, testLogger()); err == nil {
		t.Fatal("expected listen error")
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, Config{Addr: "127.0.0.1:0"}, testLogger()) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not stop")
	}
}
