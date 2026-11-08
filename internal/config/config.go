// Package config loads and validates process configuration from the
// environment. It is read once at startup in cmd/api and passed down
// explicitly; nothing else in the app reads environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Env names the deployment environment. It changes logging format, Gin
// mode and (later) cookie security, so it is validated, not free text.
type Env string

const (
	EnvDevelopment Env = "development"
	EnvTest        Env = "test"
	EnvProduction  Env = "production"
)

type Config struct {
	Env Env
	// Host is the interface to listen on. Empty means every interface (the
	// development default); production sets 127.0.0.1 so only Next.js on the
	// same server can reach the API.
	Host        string
	Port        string
	DatabaseURL string
	HTTP        HTTPConfig
	SessionTTL  time.Duration
	// StorageDir is where uploaded media files live (local disk store).
	StorageDir string
	// AllowedOrigins are browser origins allowed to make state-changing
	// requests (CSRF defence), e.g. the Next.js dev server.
	AllowedOrigins []string
	// TrustedProxies are the Next.js server IPs/CIDRs from which Gin may
	// accept forwarding headers. An empty list trusts nobody.
	TrustedProxies []string
	// EnableCORS is only needed when the browser intentionally calls Go on
	// another same-site origin instead of using the Next.js rewrite.
	EnableCORS bool
}

// CookieSecure reports whether cookies must carry the Secure flag. Only
// production is assumed to be served over HTTPS.
func (c Config) CookieSecure() bool { return c.Env == EnvProduction }

// HTTPConfig holds http.Server timeouts. Without them a slow or malicious
// client can hold a connection (and a goroutine) open indefinitely.
type HTTPConfig struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

// Load reads a .env file from the working directory (development only)
// and then builds a Config from the real environment.
//
// Real environment variables always win: godotenv.Load never overwrites a
// variable that is already set.
func Load() (Config, error) {
	if os.Getenv("APP_ENV") == "" || os.Getenv("APP_ENV") == string(EnvDevelopment) {
		if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("load .env: %w", err)
		}
	}
	return FromLookup(os.LookupEnv)
}

// FromLookup builds a Config from any key lookup function. Tests pass a
// map-backed lookup so they never touch the real process environment.
//
// All problems are collected and returned together (errors.Join), so one
// failed startup shows every misconfigured variable at once.
func FromLookup(lookup func(string) (string, bool)) (Config, error) {
	var errs []error

	get := func(key, fallback string) string {
		if v, ok := lookup(key); ok && v != "" {
			return v
		}
		return fallback
	}
	duration := func(key string, fallback time.Duration) time.Duration {
		raw := get(key, "")
		if raw == "" {
			return fallback
		}
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s: must be a positive duration like 10s, got %q", key, raw))
			return fallback
		}
		return d
	}
	boolean := func(key string, fallback bool) bool {
		raw := get(key, "")
		if raw == "" {
			return fallback
		}
		v, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: must be true or false, got %q", key, raw))
			return fallback
		}
		return v
	}

	cfg := Config{
		Env:         Env(get("APP_ENV", string(EnvDevelopment))),
		Host:        get("SERVER_HOST", ""),
		Port:        get("SERVER_PORT", "5050"),
		DatabaseURL: get("DATABASE_URL", ""),
		HTTP: HTTPConfig{
			ReadHeaderTimeout: duration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
			ReadTimeout:       duration("HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:      duration("HTTP_WRITE_TIMEOUT", 30*time.Second),
			IdleTimeout:       duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
			ShutdownTimeout:   duration("HTTP_SHUTDOWN_TIMEOUT", 10*time.Second),
		},
		SessionTTL: duration("SESSION_TTL", 7*24*time.Hour),
		StorageDir: get("STORAGE_DIR", "storage"),
		EnableCORS: boolean("ENABLE_CORS", false),
	}

	for _, raw := range strings.Split(get("ALLOWED_ORIGINS", "http://localhost:3000"), ",") {
		origin := strings.TrimSpace(raw)
		if origin == "" {
			continue
		}
		// An origin is exactly scheme://host[:port] — no path, no trailing
		// slash — because that is what browsers send in the Origin header.
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" {
			errs = append(errs, fmt.Errorf("ALLOWED_ORIGINS: %q must look like https://example.com or http://localhost:3000", origin))
			continue
		}
		cfg.AllowedOrigins = append(cfg.AllowedOrigins, origin)
	}
	for _, raw := range strings.Split(get("TRUSTED_PROXIES", ""), ",") {
		proxy := strings.TrimSpace(raw)
		if proxy == "" {
			continue
		}
		if ip := net.ParseIP(proxy); ip == nil {
			if _, _, err := net.ParseCIDR(proxy); err != nil {
				errs = append(errs, fmt.Errorf("TRUSTED_PROXIES: %q must be an IP address or CIDR", proxy))
				continue
			}
		}
		cfg.TrustedProxies = append(cfg.TrustedProxies, proxy)
	}

	switch cfg.Env {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("APP_ENV: must be development, test or production, got %q", cfg.Env))
	}
	if port, err := strconv.Atoi(cfg.Port); err != nil || port < 1 || port > 65535 {
		errs = append(errs, fmt.Errorf("SERVER_PORT: must be a number between 1 and 65535, got %q", cfg.Port))
	}
	if cfg.Host != "" && net.ParseIP(cfg.Host) == nil && cfg.Host != "localhost" {
		errs = append(errs, fmt.Errorf("SERVER_HOST: must be an IP address like 127.0.0.1 or localhost, got %q", cfg.Host))
	}
	// In production the session cookie is Secure (HTTPS only), so a browser
	// on an http:// origin could never stay signed in. Catch that at startup.
	if cfg.Env == EnvProduction {
		for _, origin := range cfg.AllowedOrigins {
			if !strings.HasPrefix(origin, "https://") {
				errs = append(errs, fmt.Errorf("ALLOWED_ORIGINS: %q must use https:// when APP_ENV=production", origin))
			}
		}
	}
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL: required"))
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

// Addr is the listen address. net.JoinHostPort adds the brackets an IPv6
// host needs ("[::1]:5050"); an empty host gives ":5050", every interface.
func (c Config) Addr() string { return net.JoinHostPort(c.Host, c.Port) }
