// Package config loads runtime configuration from environment variables.
//
// The package is intentionally tiny — a single Config struct and a
// Load function. There is no support for config files or remote
// config sources because the running environment (12-factor style,
// docker compose, Kubernetes) already supplies env vars, and adding
// more sources would muddy the precedence rules without earning any
// real flexibility.
package config

import (
	"fmt"
	"os"
)

// Config is the fully resolved runtime configuration. cmd/server
// constructs a Config via Load at startup and passes it into run().
// Tests build a Config literal directly.
type Config struct {
	// DatabaseURL is the Postgres DSN. Required.
	// Format: postgres://user:pass@host:port/db?sslmode=disable
	DatabaseURL string

	// HTTPAddr is the listen address of the public API server.
	// Defaults to ":8080" if not set.
	HTTPAddr string

	// DebugAddr controls the optional pprof admin listener. Empty
	// means disabled — the default — so pprof is NOT exposed unless
	// the operator explicitly opts in. If set, it should normally
	// be a loopback address like "127.0.0.1:6060". See the security
	// note on internal/handler/debug.go and AGENTS.md §S-16.
	DebugAddr string
}

// Load reads configuration from environment variables.
//
// Env vars:
//
//	DATABASE_URL  required, Postgres DSN
//	HTTP_ADDR     optional, defaults to ":8080"
//	DEBUG_ADDR    optional, empty by default. Set to "127.0.0.1:6060"
//	              to enable the pprof admin listener on loopback.
//
// Returns an error only when DATABASE_URL is missing — everything
// else has a safe default.
func Load() (Config, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	debugAddr := os.Getenv("DEBUG_ADDR") // empty = disabled
	return Config{
		DatabaseURL: dbURL,
		HTTPAddr:    addr,
		DebugAddr:   debugAddr,
	}, nil
}
