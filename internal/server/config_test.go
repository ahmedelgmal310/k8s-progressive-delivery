package server

import (
	"testing"
	"time"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestConfigFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		defaults Defaults
		want     Config
		wantErr  bool
	}{
		{
			name: "defaults",
			want: Config{Addr: ":8080", Version: "dev"},
		},
		{
			name:     "build-time fault defaults",
			defaults: Defaults{Version: "v3", ErrorRate: "0.3", LatencyMs: "250"},
			want:     Config{Addr: ":8080", Version: "v3", ErrorRate: 0.3, Latency: 250 * time.Millisecond},
		},
		{
			name:     "env overrides build-time defaults",
			env:      map[string]string{EnvPort: "9090", EnvFaultErrorRate: "0", EnvFaultLatencyMs: "10"},
			defaults: Defaults{Version: "v3", ErrorRate: "0.3", LatencyMs: "250"},
			want:     Config{Addr: ":9090", Version: "v3", ErrorRate: 0, Latency: 10 * time.Millisecond},
		},
		{name: "rate not a number", env: map[string]string{EnvFaultErrorRate: "lots"}, wantErr: true},
		{name: "rate above one", env: map[string]string{EnvFaultErrorRate: "1.5"}, wantErr: true},
		{name: "rate negative", env: map[string]string{EnvFaultErrorRate: "-0.1"}, wantErr: true},
		{name: "latency not a number", env: map[string]string{EnvFaultLatencyMs: "slow"}, wantErr: true},
		{name: "latency negative", env: map[string]string{EnvFaultLatencyMs: "-1"}, wantErr: true},
		{name: "latency too large", env: map[string]string{EnvFaultLatencyMs: "60001"}, wantErr: true},
		{name: "port out of range", env: map[string]string{EnvPort: "70000"}, wantErr: true},
		{name: "port not a number", env: map[string]string{EnvPort: "http"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConfigFromEnv(envFrom(tt.env), tt.defaults)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got config %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
