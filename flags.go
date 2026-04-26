package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variable name constants — single source of truth (S1).
const (
	envMSSQLAllowWrite       = "MSSQL_ALLOW_WRITE"
	envMSSQLAllowDDL         = "MSSQL_ALLOW_DDL"
	envMSSQLMaxWriteRows     = "MSSQL_MAX_WRITE_ROWS"
	envMSSQLWriteTimeout     = "MSSQL_WRITE_TIMEOUT"
	envMSSQLAuditRedact      = "MSSQL_AUDIT_REDACT_LITERALS"
	envMSSQLAuditTruncateSQL = "MSSQL_AUDIT_TRUNCATE_SQL"
)

// WriteConfig holds the runtime configuration for execute_write.
// Built in main.go after flag.Parse(); passed by value into registerTools.
// flagSourceMap records which source ("flag", "env", or "default") was used for
// each config field — consumed by printStartupBanner (S7).
type WriteConfig struct {
	AllowWrite       bool          // --allow-write or MSSQL_ALLOW_WRITE
	AllowDDL         bool          // --allow-ddl or MSSQL_ALLOW_DDL
	MaxRows          int           // --max-write-rows or MSSQL_MAX_WRITE_ROWS (default 1000)
	WriteTimeout     time.Duration // --write-timeout or MSSQL_WRITE_TIMEOUT (default 30s)
	AuditRedact      bool          // --audit-redact-literals or MSSQL_AUDIT_REDACT_LITERALS (default false)
	AuditTruncateSQL int           // --audit-truncate-sql N (FR-031, default 0 = no truncation)
	flagSourceMap    map[string]string // key: config field name, value: "flag"|"env"|"default"
}

// source returns the source for the given config field name, defaulting to "default".
func (c WriteConfig) source(field string) string {
	if c.flagSourceMap == nil {
		return "default"
	}
	if s, ok := c.flagSourceMap[field]; ok {
		return s
	}
	return "default"
}

// knownMSSQLEnvVars is the set of recognized MSSQL_* environment variables.
// Any MSSQL_* variable NOT in this list triggers a warning (FR-032).
var knownMSSQLEnvVars = map[string]struct{}{
	"MSSQL_CONNECTION_STRING": {},
	envMSSQLAllowWrite:        {},
	envMSSQLAllowDDL:          {},
	envMSSQLMaxWriteRows:      {},
	envMSSQLWriteTimeout:      {},
	envMSSQLAuditRedact:       {},
	envMSSQLAuditTruncateSQL:  {},
}

// flag variables — defined here so buildWriteConfig can read them after flag.Parse()
var (
	flagAllowWrite       bool
	flagAllowDDL         bool
	flagMaxWriteRows     int
	flagWriteTimeout     string
	flagAuditRedact      bool
	flagAuditTruncateSQL int
)

// defineFlags registers all CLI flags. Call before flag.Parse().
func defineFlags() {
	flag.BoolVar(&flagAllowWrite, "allow-write", false, "Enable the execute_write tool for DML (INSERT/UPDATE/DELETE/MERGE)")
	flag.BoolVar(&flagAllowDDL, "allow-ddl", false, "Also enable whitelisted DDL (CREATE/ALTER/DROP TABLE/INDEX, TRUNCATE TABLE); requires --allow-write")
	flag.IntVar(&flagMaxWriteRows, "max-write-rows", 0, "Maximum rows affected per DML statement (default 1000; 0 means use default)")
	flag.StringVar(&flagWriteTimeout, "write-timeout", "", "Write statement timeout, e.g. 30s (default 30s; empty means use default)")
	flag.BoolVar(&flagAuditRedact, "audit-redact-literals", false, "Replace literal values with ? in the audit log sql field")
	flag.IntVar(&flagAuditTruncateSQL, "audit-truncate-sql", 0, "Truncate sql field in audit log to N characters (0 = no truncation)")
}

// parseBoolEnv parses a boolean environment variable value ("true", "1", "yes" → true; "false", "0", "" → false).
func parseBoolEnv(val string) bool {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// isFlagSet returns true if the named flag was explicitly set on the command line.
func isFlagSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// resolveBoolFlag applies the flag > env > default precedence for a boolean config field.
// Returns the resolved value and the source ("flag", "env", or "default").
func resolveBoolFlag(flagName string, flagSet bool, flagVal bool, envName string, defaultVal bool) (val bool, src string) {
	if flagSet {
		return flagVal, "flag"
	}
	if envVal := os.Getenv(envName); envVal != "" {
		return parseBoolEnv(envVal), "env"
	}
	return defaultVal, "default"
}

// resolveIntFlag applies the flag > env > default precedence for an integer config field.
// Returns the resolved value, source, and any parse error encountered for the env value.
// A parse error causes a warning to be emitted to stderr and the default to be used (H-001).
func resolveIntFlag(flagName string, flagSet bool, flagVal int, envName string, defaultVal int) (val int, src string) {
	if flagSet {
		// Flag was explicitly set — flag takes precedence regardless of value (W1 fix).
		return flagVal, "flag"
	}
	if envVal := os.Getenv(envName); envVal != "" {
		n, err := strconv.Atoi(envVal)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: invalid %s value %q: %v; using default %d\n", envName, envVal, err, defaultVal)
			return defaultVal, "default"
		}
		return n, "env"
	}
	return defaultVal, "default"
}

// resolveDurationFlag applies the flag > env > default precedence for a duration config field.
// Returns the resolved value, source, and any parse error encountered.
// A parse error for the flag or env value causes a warning to be emitted and the default to be used (H-001/L-003).
func resolveDurationFlag(flagName string, flagSet bool, flagVal string, envName string, defaultVal time.Duration) (val time.Duration, src string) {
	if flagSet && flagVal != "" {
		d, err := time.ParseDuration(flagVal)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: invalid --%s value %q: %v; using default %s\n", flagName, flagVal, err, defaultVal)
			return defaultVal, "default"
		}
		return d, "flag"
	}
	if envVal := os.Getenv(envName); envVal != "" {
		d, err := time.ParseDuration(envVal)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: invalid %s value %q: %v; using default %s\n", envName, envVal, err, defaultVal)
			return defaultVal, "default"
		}
		return d, "env"
	}
	return defaultVal, "default"
}

// buildWriteConfig constructs a WriteConfig from parsed flags and environment variables.
// Flag values take precedence over env vars (D9).
// Call after flag.Parse() and defineFlags().
func buildWriteConfig() WriteConfig {
	cfg := WriteConfig{
		flagSourceMap: make(map[string]string),
	}

	// --- AllowWrite ---
	cfg.AllowWrite, cfg.flagSourceMap["AllowWrite"] = resolveBoolFlag(
		"allow-write", isFlagSet("allow-write"), flagAllowWrite,
		envMSSQLAllowWrite, false,
	)

	// --- AllowDDL ---
	cfg.AllowDDL, cfg.flagSourceMap["AllowDDL"] = resolveBoolFlag(
		"allow-ddl", isFlagSet("allow-ddl"), flagAllowDDL,
		envMSSQLAllowDDL, false,
	)

	// --- MaxRows ---
	cfg.MaxRows, cfg.flagSourceMap["MaxRows"] = resolveIntFlag(
		"max-write-rows", isFlagSet("max-write-rows"), flagMaxWriteRows,
		envMSSQLMaxWriteRows, 1000,
	)

	// --- WriteTimeout ---
	cfg.WriteTimeout, cfg.flagSourceMap["WriteTimeout"] = resolveDurationFlag(
		"write-timeout", isFlagSet("write-timeout"), flagWriteTimeout,
		envMSSQLWriteTimeout, 30*time.Second,
	)

	// --- AuditRedact ---
	cfg.AuditRedact, cfg.flagSourceMap["AuditRedact"] = resolveBoolFlag(
		"audit-redact-literals", isFlagSet("audit-redact-literals"), flagAuditRedact,
		envMSSQLAuditRedact, false,
	)

	// --- AuditTruncateSQL ---
	cfg.AuditTruncateSQL, cfg.flagSourceMap["AuditTruncateSQL"] = resolveIntFlag(
		"audit-truncate-sql", isFlagSet("audit-truncate-sql"), flagAuditTruncateSQL,
		envMSSQLAuditTruncateSQL, 0,
	)

	return cfg
}

// validateWriteConfig validates the WriteConfig for invalid / inconsistent settings.
// Returns an error on invalid config; the caller should exit(1) with the error message.
func validateWriteConfig(cfg WriteConfig) error {
	// Two-tier flag gate: --allow-ddl requires --allow-write (FR-004, D7)
	if cfg.AllowDDL && !cfg.AllowWrite {
		return fmt.Errorf("--allow-ddl requires --allow-write; start the server with both flags or remove --allow-ddl")
	}

	// MaxRows must be positive when write is enabled
	if cfg.AllowWrite && cfg.MaxRows <= 0 {
		return fmt.Errorf("--max-write-rows must be a positive integer (got: %d)", cfg.MaxRows)
	}

	// WriteTimeout must be positive when write is enabled
	if cfg.AllowWrite && cfg.WriteTimeout <= 0 {
		return fmt.Errorf("--write-timeout must be a positive duration (got: %v)", cfg.WriteTimeout)
	}

	return nil
}

// printStartupBanner emits the FR-021 startup banner to w when cfg.AllowWrite is true.
func printStartupBanner(w io.Writer, cfg WriteConfig) {
	if !cfg.AllowWrite {
		return
	}

	ddlStr := "off"
	if cfg.AllowDDL {
		ddlStr = "on"
	}
	auditStr := "off"
	if cfg.AuditRedact {
		auditStr = "on"
	}

	fmt.Fprintf(w, "execute_write enabled, ddl=%s(source=%s), max_write_rows=%d(source=%s), write_timeout=%s(source=%s), audit_redact=%s(source=%s)\n",
		ddlStr, cfg.source("AllowDDL"),
		cfg.MaxRows, cfg.source("MaxRows"),
		cfg.WriteTimeout.String(), cfg.source("WriteTimeout"),
		auditStr, cfg.source("AuditRedact"),
	)

	// Recommend --audit-redact-literals if not already set
	if !cfg.AuditRedact {
		fmt.Fprintf(w, "Recommendation: use --audit-redact-literals if audit output is captured to a less-trusted log sink to prevent PII/credentials leakage.\n")
	}

	// M-001: Advisory warning about audit-line interleaving above PIPE_BUF.
	// Emitted when --allow-write is on but --audit-truncate-sql is not set.
	if cfg.AuditTruncateSQL == 0 {
		fmt.Fprintf(w, "warning: audit lines are not atomically written above ~4000 bytes on most systems.\n")
		fmt.Fprintf(w, "warning: consider --audit-truncate-sql 3500 for high-concurrency deployments.\n")
	}
}

// warnUnknownEnv writes a warning for any MSSQL_* environment variable that is not
// in the known set. This helps detect typos like MSSQL_ALLOW_WRTE (FR-032).
func warnUnknownEnv(w io.Writer) {
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) < 1 {
			continue
		}
		name := parts[0]
		if strings.HasPrefix(name, "MSSQL_") {
			if _, ok := knownMSSQLEnvVars[name]; !ok {
				fmt.Fprintf(w, "warning: unrecognized environment variable %q (possible typo); known MSSQL_* vars: MSSQL_CONNECTION_STRING, %s, %s, %s, %s, %s, %s\n",
					name,
					envMSSQLAllowWrite, envMSSQLAllowDDL, envMSSQLMaxWriteRows,
					envMSSQLWriteTimeout, envMSSQLAuditRedact, envMSSQLAuditTruncateSQL,
				)
			}
		}
	}
}
