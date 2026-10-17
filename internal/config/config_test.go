package config

import (
	"strings"
	"testing"
	"time"
)

func lookupFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestFromLookup_Defaults(t *testing.T) {
	cfg, err := FromLookup(lookupFrom(map[string]string{
		"DATABASE_URL": "postgres://localhost/db",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != EnvDevelopment {
		t.Errorf("Env = %q, want %q", cfg.Env, EnvDevelopment)
	}
	if cfg.Port != "5050" {
		t.Errorf("Port = %q, want 5050", cfg.Port)
	}
	if cfg.HTTP.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 5s", cfg.HTTP.ReadHeaderTimeout)
	}
	if cfg.Addr() != ":5050" {
		t.Errorf("Addr() = %q, want :5050", cfg.Addr())
	}
}

func TestFromLookup_Overrides(t *testing.T) {
	cfg, err := FromLookup(lookupFrom(map[string]string{
		"APP_ENV":               "production",
		"SERVER_PORT":           "9000",
		"DATABASE_URL":          "postgres://localhost/db",
		"HTTP_SHUTDOWN_TIMEOUT": "3s",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != EnvProduction || cfg.Port != "9000" || cfg.HTTP.ShutdownTimeout != 3*time.Second {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestFromLookup_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr []string // substrings that must all appear
	}{
		{
			name:    "missing database url",
			env:     map[string]string{},
			wantErr: []string{"DATABASE_URL"},
		},
		{
			name:    "bad env",
			env:     map[string]string{"DATABASE_URL": "x", "APP_ENV": "staging"},
			wantErr: []string{"APP_ENV"},
		},
		{
			name:    "bad port",
			env:     map[string]string{"DATABASE_URL": "x", "SERVER_PORT": "abc"},
			wantErr: []string{"SERVER_PORT"},
		},
		{
			name:    "bad duration",
			env:     map[string]string{"DATABASE_URL": "x", "HTTP_READ_TIMEOUT": "fast"},
			wantErr: []string{"HTTP_READ_TIMEOUT"},
		},
		{
			name:    "reports every problem at once",
			env:     map[string]string{"APP_ENV": "staging", "SERVER_PORT": "0"},
			wantErr: []string{"APP_ENV", "SERVER_PORT", "DATABASE_URL"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromLookup(lookupFrom(tt.env))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}
