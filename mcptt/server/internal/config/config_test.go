package config

import (
	"strings"
	"testing"
	"time"
)

const validSecret = "0123456789abcdef0123456789abcdef" // 32 bytes

func TestLoadRequiresSecret(t *testing.T) {
	if _, err := Load(); err == nil {
		t.Error("Load without MCPTT_JWT_SECRET = nil error, want error (server must refuse to boot)")
	}
}

func TestLoadRejectsShortSecret(t *testing.T) {
	t.Setenv("MCPTT_JWT_SECRET", "too-short")
	if _, err := Load(); err == nil {
		t.Error("Load with short secret = nil error, want error")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("MCPTT_JWT_SECRET", validSecret)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.DBPath != "mcptt.db" {
		t.Errorf("DBPath = %q, want mcptt.db", cfg.DBPath)
	}
	if cfg.JWTTTL != 8*time.Hour {
		t.Errorf("JWTTTL = %v, want 8h", cfg.JWTTTL)
	}
	if cfg.MaxTalkDuration != 60*time.Second {
		t.Errorf("MaxTalkDuration = %v, want 60s", cfg.MaxTalkDuration)
	}
	if cfg.SQLiteBusyTimeout != 5*time.Second {
		t.Errorf("SQLiteBusyTimeout = %v, want 5s", cfg.SQLiteBusyTimeout)
	}
	if string(cfg.JWTSecret) != validSecret {
		t.Error("JWTSecret not carried through")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("MCPTT_JWT_SECRET", validSecret)
	t.Setenv("MCPTT_ADDR", ":9090")
	t.Setenv("MCPTT_DB_PATH", "/tmp/mcptt-test.db")
	t.Setenv("MCPTT_JWT_TTL", "30m")
	t.Setenv("MCPTT_MAX_TALK_DURATION", "45s")
	t.Setenv("MCPTT_SQLITE_BUSY_TIMEOUT", "2s")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":9090" || cfg.DBPath != "/tmp/mcptt-test.db" {
		t.Errorf("addr/db override mismatch: %+v", cfg)
	}
	if cfg.JWTTTL != 30*time.Minute || cfg.MaxTalkDuration != 45*time.Second || cfg.SQLiteBusyTimeout != 2*time.Second {
		t.Errorf("duration overrides mismatch: %+v", cfg)
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	t.Setenv("MCPTT_JWT_SECRET", validSecret)
	t.Setenv("MCPTT_JWT_TTL", "banana")
	if _, err := Load(); err == nil {
		t.Error("Load with invalid MCPTT_JWT_TTL = nil error, want error")
	} else if !strings.Contains(err.Error(), "MCPTT_JWT_TTL") {
		t.Errorf("error should name the offending variable, got: %v", err)
	}
}
