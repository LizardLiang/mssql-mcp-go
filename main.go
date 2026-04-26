package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/server"
)

func main() {
	// Register and parse CLI flags before any config reading.
	defineFlags()
	flag.Parse()

	// Build config from parsed flags + environment.
	cfg := buildWriteConfig()

	// Validate config — fail fast at startup (FR-004, T4).
	if err := validateWriteConfig(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}

	// Warn about unrecognized MSSQL_* env vars (FR-032).
	warnUnknownEnv(os.Stderr)

	// Emit startup banner when write capability is enabled (FR-021).
	if cfg.AllowWrite {
		printStartupBanner(os.Stderr, cfg)
	}

	// Schedule audit-failure summary on graceful shutdown (NEW v2, Apollo Concern 5).
	defer flushAuditFailureCount(os.Stderr)

	if err := initDB(); err != nil {
		fmt.Fprintf(os.Stderr, "database init failed: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	s := server.NewMCPServer("mssql-mcp", "1.2.0")
	registerTools(s, cfg)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}
