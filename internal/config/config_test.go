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
	if cfg.SessionTTL != 7*24*time.Hour {
		t.Errorf("SessionTTL = %v, want 168h", cfg.SessionTTL)
	}
	if len(cfg.AllowedOrigins) != 1 || cfg.AllowedOrigins[0] != "http://localhost:3000" {
		t.Errorf("AllowedOrigins = %v, want [http://localhost:3000]", cfg.AllowedOrigins)
	}
	if cfg.EnableCORS || len(cfg.TrustedProxies) != 0 {
		t.Errorf("cross-origin/proxy defaults are not secure: %+v", cfg)
	}
	if cfg.CookieSecure() {
		t.Error("CookieSecure() = true in development, want false")
	}
}

func TestFromLookup_AllowedOrigins(t *testing.T) {
	cfg, err := FromLookup(lookupFrom(map[string]string{
		"DATABASE_URL":    "x",
		"ALLOWED_ORIGINS": " https://app.example.com , http://localhost:3000 ,",
		"TRUSTED_PROXIES": " 10.0.0.5 , 10.20.0.0/16 ,",
		"ENABLE_CORS":     "true",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"https://app.example.com", "http://localhost:3000"}
	if len(cfg.AllowedOrigins) != 2 || cfg.AllowedOrigins[0] != want[0] || cfg.AllowedOrigins[1] != want[1] {
		t.Errorf("AllowedOrigins = %v, want %v", cfg.AllowedOrigins, want)
	}
	if !cfg.EnableCORS || len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[0] != "10.0.0.5" || cfg.TrustedProxies[1] != "10.20.0.0/16" {
		t.Errorf("proxy/CORS config not parsed: %+v", cfg)
	}
}

func TestFromLookup_Overrides(t *testing.T) {
	cfg, err := FromLookup(lookupFrom(map[string]string{
		"APP_ENV":               "production",
		"SERVER_HOST":           "127.0.0.1",
		"SERVER_PORT":           "9000",
		"DATABASE_URL":          "postgres://localhost/db",
		"HTTP_SHUTDOWN_TIMEOUT": "3s",
		"ALLOWED_ORIGINS":       "https://app.example.com",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != EnvProduction || cfg.Port != "9000" || cfg.HTTP.ShutdownTimeout != 3*time.Second {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if got := cfg.Addr(); got != "127.0.0.1:9000" {
		t.Errorf("Addr() = %q, want 127.0.0.1:9000", got)
	}
}

func TestAddr(t *testing.T) {
	for host, want := range map[string]string{"": ":5050", "127.0.0.1": "127.0.0.1:5050", "::1": "[::1]:5050", "localhost": "localhost:5050"} {
		if got := (Config{Host: host, Port: "5050"}).Addr(); got != want {
			t.Errorf("Addr() with host %q = %q, want %q", host, got, want)
		}
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
			name:    "origin with path",
			env:     map[string]string{"DATABASE_URL": "x", "ALLOWED_ORIGINS": "http://localhost:3000/"},
			wantErr: []string{"ALLOWED_ORIGINS"},
		},
		{
			name:    "origin without scheme",
			env:     map[string]string{"DATABASE_URL": "x", "ALLOWED_ORIGINS": "localhost:3000"},
			wantErr: []string{"ALLOWED_ORIGINS"},
		},
		{
			name:    "bad trusted proxy",
			env:     map[string]string{"DATABASE_URL": "x", "TRUSTED_PROXIES": "next.internal"},
			wantErr: []string{"TRUSTED_PROXIES"},
		},
		{
			name:    "bad cors boolean",
			env:     map[string]string{"DATABASE_URL": "x", "ENABLE_CORS": "sometimes"},
			wantErr: []string{"ENABLE_CORS"},
		},
		{
			name:    "bad server host",
			env:     map[string]string{"DATABASE_URL": "x", "SERVER_HOST": "api.internal"},
			wantErr: []string{"SERVER_HOST"},
		},
		{
			name:    "http origin in production",
			env:     map[string]string{"DATABASE_URL": "x", "APP_ENV": "production", "ALLOWED_ORIGINS": "https://app.example.com,http://app.example.com"},
			wantErr: []string{`"http://app.example.com" must use https://`},
		},
		{
			name:    "default localhost origin in production",
			env:     map[string]string{"DATABASE_URL": "x", "APP_ENV": "production"},
			wantErr: []string{"ALLOWED_ORIGINS", "APP_ENV=production"},
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
