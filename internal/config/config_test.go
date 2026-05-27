package config

import "testing"

// TestLoad_DefaultsHTTPAddr verifies that when HTTP_ADDR is empty,
// Load picks the documented default (":8080"). DATABASE_URL is the
// only required var — everything else has a sensible default.
//
// t.Setenv automatically unsets the var when the test ends so
// concurrent tests don't see polluted state.
func TestLoad_DefaultsHTTPAddr(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("HTTP_ADDR", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DatabaseURL != "postgres://x" {
		t.Fatalf("dsn: %q", cfg.DatabaseURL)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("addr: %q", cfg.HTTPAddr)
	}
}

// TestLoad_CustomHTTPAddr verifies that a non-empty HTTP_ADDR is
// passed through unchanged. Together with DefaultsHTTPAddr this
// exercises both branches of the default-or-override logic.
func TestLoad_CustomHTTPAddr(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("HTTP_ADDR", ":9999")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HTTPAddr != ":9999" {
		t.Fatalf("addr: %q", cfg.HTTPAddr)
	}
}

// TestLoad_RequiresDatabaseURL verifies the only hard requirement:
// without DATABASE_URL the service cannot start, and Load must say
// so explicitly rather than returning a partially-built config.
func TestLoad_RequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when DATABASE_URL is empty")
	}
}

// TestLoad_DebugAddrDefaultsDisabled is a security regression test.
// The pprof admin listener MUST be opt-in — leaving DEBUG_ADDR unset
// or empty must produce a Config with DebugAddr == "" so cmd/server
// never starts the debug listener by accident.
func TestLoad_DebugAddrDefaultsDisabled(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("DEBUG_ADDR", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DebugAddr != "" {
		t.Fatalf("debug addr should default to empty (disabled), got %q", cfg.DebugAddr)
	}
}

// TestLoad_DebugAddrCustom verifies that an explicit DEBUG_ADDR is
// honoured. The format check itself (loopback vs not) lives in
// cmd/server.isLoopbackAddr and has its own test.
func TestLoad_DebugAddrCustom(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("DEBUG_ADDR", "127.0.0.1:6060")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DebugAddr != "127.0.0.1:6060" {
		t.Fatalf("debug addr: %q", cfg.DebugAddr)
	}
}
