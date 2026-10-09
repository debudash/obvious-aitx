// Package config loads MCPTT server configuration from the environment.
//
// Posture: the JWT secret is required — the server refuses to boot with an
// insecure token store. Every other knob has a deployment-sane default so a
// dev server is `MCPTT_JWT_SECRET=$(openssl rand -hex 32) go run ./cmd/server`.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// Config is the resolved server configuration.
type Config struct {
	// Addr is the HTTP + WebSocket listen address.
	Addr string
	// DBPath is the SQLite database file path.
	DBPath string
	// JWTSecret signs access tokens (HS256); required, >= 32 bytes.
	JWTSecret []byte
	// JWTTTL is the access-token lifetime. Default 8h — one operational shift.
	JWTTTL time.Duration
	// MaxTalkDuration bounds every non-emergency talk burst (spec: 60s default,
	// enforced by the floor controller, never by clients).
	MaxTalkDuration time.Duration
	// SQLiteBusyTimeout bounds how long a statement waits on the write lock.
	SQLiteBusyTimeout time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		Addr:            env("MCPTT_ADDR", ":8080"),
		DBPath:          env("MCPTT_DB_PATH", "mcptt.db"),
		JWTTTL:          8 * time.Hour,
		MaxTalkDuration: 60 * time.Second,
	}
	var errs []error

	var err error
	cfg.JWTTTL, err = envDuration("MCPTT_JWT_TTL", cfg.JWTTTL)
	if err != nil {
		errs = append(errs, fmt.Errorf("MCPTT_JWT_TTL: %w", err))
	}
	cfg.MaxTalkDuration, err = envDuration("MCPTT_MAX_TALK_DURATION", cfg.MaxTalkDuration)
	if err != nil {
		errs = append(errs, fmt.Errorf("MCPTT_MAX_TALK_DURATION: %w", err))
	}
	cfg.SQLiteBusyTimeout, err = envDuration("MCPTT_SQLITE_BUSY_TIMEOUT", 5*time.Second)
	if err != nil {
		errs = append(errs, fmt.Errorf("MCPTT_SQLITE_BUSY_TIMEOUT: %w", err))
	}

	secret := os.Getenv("MCPTT_JWT_SECRET")
	switch {
	case secret == "":
		errs = append(errs, errors.New("MCPTT_JWT_SECRET is required (generate one with `openssl rand -hex 32`)"))
	case len(secret) < 32:
		errs = append(errs, fmt.Errorf("MCPTT_JWT_SECRET must be at least 32 bytes, got %d", len(secret)))
	default:
		cfg.JWTSecret = []byte(secret)
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	return d, nil
}
