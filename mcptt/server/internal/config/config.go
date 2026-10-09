// Package config loads MCPTT server configuration from the environment.
//
// Posture: the JWT secret is required — the server refuses to boot with an
// insecure token store. Every other knob has a deployment-sane default so a
// dev server is `MCPTT_JWT_SECRET=$(openssl rand -hex 32) go run ./cmd/server`.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
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

	// Carrier is the [carrier] section: the flag-gated RX integration
	// towards a carrier endpoint. Disabled by default — when disabled, no
	// integration code path runs and the server opens zero external sockets.
	Carrier CarrierConfig
}

// CarrierConfig mirrors the spec's [carrier] flag table. Every key maps to
// an MCPTT_CARRIER_* environment variable (the deployment's config surface);
// unknown MCPTT_CARRIER_* variables fail closed at startup — a misspelled
// key must never read as "integration quietly off".
type CarrierConfig struct {
	// Enabled is the master switch (carrier.enabled). False — the shipped
	// default — means no integration code path runs at all.
	Enabled bool
	// Adapter selects the streaming adapter (carrier.adapter): "rtp"
	// (receive-only RTP audio) or "webhook" (JSON events only).
	Adapter string
	// Endpoint is the carrier RX host:port for the RTP adapter
	// (carrier.endpoint). Required when enabled with the rtp adapter.
	Endpoint string
	// Codec is the audio format (carrier.codec): "pcmu" (G.711 µ-law,
	// payload type 0) or "opus" (payload type 96). Default "pcmu".
	Codec string
	// Groups is the talkgroup allowlist (carrier.groups) as group IDs.
	// Only flagged groups are streamed; empty streams nothing — flagging
	// the master switch alone never enables a feed.
	Groups []string
	// EventsURL optionally receives JSON call/PTT/emergency events
	// (carrier.events_url) — independent of the adapter selection.
	EventsURL string
}

const carrierEnvPrefix = "MCPTT_CARRIER_"

// carrierEnvKeys are the known MCPTT_CARRIER_* variables. Anything with the
// prefix but not in this set is rejected at load.
var carrierEnvKeys = []string{
	"MCPTT_CARRIER_ENABLED",
	"MCPTT_CARRIER_ADAPTER",
	"MCPTT_CARRIER_ENDPOINT",
	"MCPTT_CARRIER_CODEC",
	"MCPTT_CARRIER_GROUPS",
	"MCPTT_CARRIER_EVENTS_URL",
}

// loadCarrier parses and validates the [carrier] section.
func loadCarrier() (CarrierConfig, error) {
	var errs []error
	c := CarrierConfig{
		Adapter: "rtp",
		Codec:   "pcmu",
	}

	if raw := os.Getenv("MCPTT_CARRIER_ENABLED"); raw != "" {
		switch strings.ToLower(raw) {
		case "true", "1", "on":
			c.Enabled = true
		case "false", "0", "off":
			c.Enabled = false
		default:
			errs = append(errs, fmt.Errorf("MCPTT_CARRIER_ENABLED: invalid boolean %q (use true/false)", raw))
		}
	}
	if raw := os.Getenv("MCPTT_CARRIER_ADAPTER"); raw != "" {
		c.Adapter = strings.ToLower(strings.TrimSpace(raw))
	}
	c.Endpoint = strings.TrimSpace(os.Getenv("MCPTT_CARRIER_ENDPOINT"))
	if raw := os.Getenv("MCPTT_CARRIER_CODEC"); raw != "" {
		c.Codec = strings.ToLower(strings.TrimSpace(raw))
	}
	if raw := os.Getenv("MCPTT_CARRIER_GROUPS"); raw != "" {
		for _, g := range strings.Split(raw, ",") {
			if g = strings.TrimSpace(g); g != "" {
				c.Groups = append(c.Groups, g)
			}
		}
	}
	c.EventsURL = strings.TrimSpace(os.Getenv("MCPTT_CARRIER_EVENTS_URL"))

	// Fail closed on unknown keys.
	var unknown []string
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(k, carrierEnvPrefix) || k == carrierEnvPrefix {
			continue
		}
		known := false
		for _, want := range carrierEnvKeys {
			if k == want {
				known = true
				break
			}
		}
		if !known {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		errs = append(errs, fmt.Errorf("unknown carrier config keys (allowed: %s): %s",
			strings.Join(carrierEnvKeys, ", "), strings.Join(unknown, ", ")))
	}

	switch c.Adapter {
	case "rtp", "webhook":
	default:
		errs = append(errs, fmt.Errorf("MCPTT_CARRIER_ADAPTER: %q is not rtp or webhook", c.Adapter))
	}
	switch c.Codec {
	case "pcmu", "opus":
	default:
		errs = append(errs, fmt.Errorf("MCPTT_CARRIER_CODEC: %q is not pcmu or opus", c.Codec))
	}
	if c.EventsURL != "" {
		u, err := url.Parse(c.EventsURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("MCPTT_CARRIER_EVENTS_URL: %q is not an http(s) URL", c.EventsURL))
		}
	}
	if c.Enabled {
		switch c.Adapter {
		case "rtp":
			if _, _, err := net.SplitHostPort(c.Endpoint); err != nil {
				errs = append(errs, fmt.Errorf("MCPTT_CARRIER_ENDPOINT: %q is not host:port", c.Endpoint))
			}
		case "webhook":
			if c.EventsURL == "" {
				errs = append(errs, errors.New("MCPTT_CARRIER_EVENTS_URL is required when the adapter is webhook"))
			}
		}
	}

	if len(errs) > 0 {
		return CarrierConfig{}, errors.Join(errs...)
	}
	return c, nil
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

	carrier, err := loadCarrier()
	if err != nil {
		errs = append(errs, err)
	} else {
		cfg.Carrier = carrier
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
