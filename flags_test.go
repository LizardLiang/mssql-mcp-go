package main

import (
	"bytes"
	"flag"
	"os"
	"strings"
	"testing"
	"time"
)

// resetFlags resets all package-level flag variables to their zero values.
// Also resets the default flag.CommandLine so flag.Visit works correctly.
func resetFlags() {
	flagAllowWrite = false
	flagAllowDDL = false
	flagMaxWriteRows = 0
	flagWriteTimeout = ""
	flagAuditRedact = false
	flagAuditTruncateSQL = 0
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	defineFlags()
}

// clearEnv removes all known MSSQL_* env vars used by buildWriteConfig.
func clearEnv() {
	os.Unsetenv(envMSSQLAllowWrite)
	os.Unsetenv(envMSSQLAllowDDL)
	os.Unsetenv(envMSSQLMaxWriteRows)
	os.Unsetenv(envMSSQLWriteTimeout)
	os.Unsetenv(envMSSQLAuditRedact)
	os.Unsetenv(envMSSQLAuditTruncateSQL)
}

// makeWriteConfig constructs a WriteConfig with a flagSourceMap pre-populated.
// Used in tests that pass a WriteConfig with source information to printStartupBanner.
func makeWriteConfig(base WriteConfig, sources map[string]string) WriteConfig {
	base.flagSourceMap = make(map[string]string)
	for k, v := range sources {
		base.flagSourceMap[k] = v
	}
	return base
}

// ---- TestValidateWriteConfig ----

func TestValidateWriteConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id      string
		cfg     WriteConfig
		wantErr bool
		errText string
	}{
		{
			id:      "TC-CFG-001",
			cfg:     WriteConfig{AllowDDL: true, AllowWrite: false},
			wantErr: true,
			errText: "--allow-ddl",
		},
		{
			id:      "TC-CFG-001b",
			cfg:     WriteConfig{AllowDDL: true, AllowWrite: false},
			wantErr: true,
			errText: "--allow-write",
		},
		{
			id:      "TC-CFG-002",
			cfg:     WriteConfig{AllowWrite: true, MaxRows: 0, WriteTimeout: 30 * time.Second},
			wantErr: true,
			errText: "--max-write-rows must be a positive integer",
		},
		{
			id:      "TC-CFG-003",
			cfg:     WriteConfig{AllowWrite: true, MaxRows: -1, WriteTimeout: 30 * time.Second},
			wantErr: true,
			errText: "--max-write-rows must be a positive integer",
		},
		{
			id:      "TC-CFG-004",
			cfg:     WriteConfig{AllowWrite: true, MaxRows: 1000, WriteTimeout: 0},
			wantErr: true,
			errText: "--write-timeout must be a positive duration",
		},
		{
			id:      "TC-CFG-005",
			cfg:     WriteConfig{AllowWrite: true, MaxRows: 1000, WriteTimeout: -1 * time.Nanosecond},
			wantErr: true,
			errText: "--write-timeout must be a positive duration",
		},
		{
			id:      "TC-CFG-006",
			cfg:     WriteConfig{AllowWrite: false, AllowDDL: false},
			wantErr: false,
		},
		{
			id:      "TC-CFG-007",
			cfg:     WriteConfig{AllowWrite: true, MaxRows: 1, WriteTimeout: 1 * time.Nanosecond},
			wantErr: false,
		},
		{
			id:      "TC-CFG-008",
			cfg:     WriteConfig{AllowWrite: true, AllowDDL: true, MaxRows: 1000, WriteTimeout: 30 * time.Second},
			wantErr: false,
		},
		{
			id:      "TC-CFG-009",
			cfg:     WriteConfig{AllowWrite: true, MaxRows: 1, WriteTimeout: 30 * time.Second},
			wantErr: false,
		},
		{
			id:      "TC-CFG-010",
			cfg:     WriteConfig{AllowWrite: true, MaxRows: 1<<31 - 1, WriteTimeout: 30 * time.Second},
			wantErr: false,
		},
		{
			id:      "TC-CFG-011",
			cfg:     WriteConfig{AllowWrite: true, MaxRows: 1000, WriteTimeout: 1 * time.Millisecond},
			wantErr: false,
		},
		{
			id:      "TC-CFG-012",
			cfg:     WriteConfig{AllowWrite: true, AllowDDL: true, MaxRows: 1000, WriteTimeout: 30 * time.Second},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()
			err := validateWriteConfig(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Errorf("%s: expected error, got nil", tt.id)
					return
				}
				if tt.errText != "" && !strings.Contains(err.Error(), tt.errText) {
					t.Errorf("%s: error %q does not contain %q", tt.id, err.Error(), tt.errText)
				}
			} else {
				if err != nil {
					t.Errorf("%s: unexpected error: %v", tt.id, err)
				}
			}
		})
	}
}

// ---- TestBuildExecuteWriteDescription ----

func TestBuildExecuteWriteDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id       string
		cfg      WriteConfig
		required []string
	}{
		{
			id:       "TC-DSC-001",
			cfg:      WriteConfig{MaxRows: 1000, WriteTimeout: 30 * time.Second},
			required: []string{"WHERE required"},
		},
		{
			id:       "TC-DSC-002",
			cfg:      WriteConfig{MaxRows: 1000, WriteTimeout: 30 * time.Second},
			required: []string{"MERGE requires ON"},
		},
		{
			id:       "TC-DSC-003",
			cfg:      WriteConfig{MaxRows: 1000, WriteTimeout: 30 * time.Second},
			required: []string{"TRUNCATE/DROP require confirm: true"},
		},
		{
			id:       "TC-DSC-004",
			cfg:      WriteConfig{MaxRows: 500, WriteTimeout: 30 * time.Second},
			required: []string{"max rows: 500"},
		},
		{
			id:       "TC-DSC-005",
			cfg:      WriteConfig{MaxRows: 1000, WriteTimeout: 1 * time.Minute},
			required: []string{"write timeout: 1m0s"},
		},
		{
			id:       "TC-DSC-006",
			cfg:      WriteConfig{MaxRows: 1000, WriteTimeout: 30 * time.Second},
			required: []string{"dry_run"},
		},
		{
			id:       "TC-DSC-007",
			cfg:      WriteConfig{MaxRows: 1000, WriteTimeout: 30 * time.Second},
			required: []string{"dry_run overrides confirm"},
		},
		{
			id:       "TC-DSC-008",
			cfg:      WriteConfig{MaxRows: 1000, WriteTimeout: 30 * time.Second},
			required: []string{"WHERE/ON detection ignores SQL comments and quoted literals"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()
			desc := buildExecuteWriteDescription(tt.cfg)
			for _, req := range tt.required {
				if !strings.Contains(desc, req) {
					t.Errorf("%s: description does not contain %q\nGot: %s", tt.id, req, desc)
				}
			}
		})
	}
}

// ---- TestPrintStartupBanner ----

func TestPrintStartupBanner(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id       string
		cfg      WriteConfig
		required []string
		empty    bool // expect nothing written
	}{
		{
			id: "TC-BNR-001",
			cfg: makeWriteConfig(WriteConfig{
				AllowWrite: true, AllowDDL: false,
				MaxRows: 1000, WriteTimeout: 30 * time.Second,
				AuditRedact: false, AuditTruncateSQL: 3500, // suppress M-001 warning
			}, map[string]string{
				"AllowDDL": "default", "MaxRows": "default",
				"WriteTimeout": "default", "AuditRedact": "default",
			}),
			required: []string{
				"execute_write enabled",
				"ddl=off(source=default)",
				"max_write_rows=1000(source=default)",
				"write_timeout=30s(source=default)",
				"audit_redact=off(source=default)",
			},
		},
		{
			id: "TC-BNR-002",
			cfg: makeWriteConfig(WriteConfig{
				AllowWrite: true, AllowDDL: true,
				MaxRows: 1000, WriteTimeout: 30 * time.Second,
				AuditRedact: false, AuditTruncateSQL: 3500,
			}, map[string]string{
				"AllowDDL": "flag", "MaxRows": "default",
				"WriteTimeout": "default", "AuditRedact": "default",
			}),
			required: []string{"ddl=on(source=flag)"},
		},
		{
			id: "TC-BNR-003",
			cfg: makeWriteConfig(WriteConfig{
				AllowWrite: true, AllowDDL: false,
				MaxRows: 500, WriteTimeout: 30 * time.Second,
				AuditRedact: false, AuditTruncateSQL: 3500,
			}, map[string]string{
				"AllowDDL": "default", "MaxRows": "env",
				"WriteTimeout": "default", "AuditRedact": "default",
			}),
			required: []string{"max_write_rows=500(source=env)"},
		},
		{
			id: "TC-BNR-004",
			cfg: makeWriteConfig(WriteConfig{
				AllowWrite: true, AllowDDL: false,
				MaxRows: 1000, WriteTimeout: 10 * time.Second,
				AuditRedact: false, AuditTruncateSQL: 3500,
			}, map[string]string{
				"AllowDDL": "default", "MaxRows": "default",
				"WriteTimeout": "flag", "AuditRedact": "default",
			}),
			required: []string{"write_timeout=10s(source=flag)"},
		},
		{
			id: "TC-BNR-005",
			cfg: makeWriteConfig(WriteConfig{
				AllowWrite: true, AllowDDL: false,
				MaxRows: 1000, WriteTimeout: 30 * time.Second,
				AuditRedact: true, AuditTruncateSQL: 3500,
			}, map[string]string{
				"AllowDDL": "default", "MaxRows": "default",
				"WriteTimeout": "default", "AuditRedact": "flag",
			}),
			required: []string{"audit_redact=on(source=flag)"},
		},
		{
			id: "TC-BNR-006",
			cfg: WriteConfig{
				AllowWrite: false,
			},
			empty: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			printStartupBanner(&buf, tt.cfg)
			out := buf.String()
			if tt.empty {
				if out != "" {
					t.Errorf("%s: expected empty output, got: %q", tt.id, out)
				}
				return
			}
			for _, req := range tt.required {
				if !strings.Contains(out, req) {
					t.Errorf("%s: banner does not contain %q\nGot: %s", tt.id, req, out)
				}
			}
		})
	}
}

// TestPrintStartupBanner_AuditInterleaveWarning verifies that the M-001 advisory
// warning is emitted when --allow-write is on and --audit-truncate-sql is 0.
func TestPrintStartupBanner_AuditInterleaveWarning(t *testing.T) {
	t.Parallel()

	t.Run("warning-present-when-no-truncate", func(t *testing.T) {
		t.Parallel()
		cfg := makeWriteConfig(WriteConfig{
			AllowWrite: true, MaxRows: 1000, WriteTimeout: 30 * time.Second,
			AuditTruncateSQL: 0, // no truncation — triggers warning
		}, map[string]string{})
		var buf bytes.Buffer
		printStartupBanner(&buf, cfg)
		out := buf.String()
		if !strings.Contains(out, "audit lines are not atomically written") {
			t.Errorf("expected audit interleave warning, got: %s", out)
		}
		if !strings.Contains(out, "--audit-truncate-sql 3500") {
			t.Errorf("expected --audit-truncate-sql 3500 recommendation, got: %s", out)
		}
	})

	t.Run("warning-absent-when-truncate-set", func(t *testing.T) {
		t.Parallel()
		cfg := makeWriteConfig(WriteConfig{
			AllowWrite: true, MaxRows: 1000, WriteTimeout: 30 * time.Second,
			AuditTruncateSQL: 3500, // truncation set — no warning
		}, map[string]string{})
		var buf bytes.Buffer
		printStartupBanner(&buf, cfg)
		out := buf.String()
		if strings.Contains(out, "audit lines are not atomically written") {
			t.Errorf("unexpected audit interleave warning when truncate is set: %s", out)
		}
	})
}

// ---- TestBuildWriteConfig_Precedence ----

// buildWriteConfigWithFlags is a test helper that simulates setting specific flags and env vars,
// then calls buildWriteConfig. It resets all flag state and env vars first.
func buildWriteConfigWithFlags(t *testing.T, args []string, env map[string]string) WriteConfig {
	t.Helper()

	// Save and restore environment
	savedEnv := make(map[string]string)
	envKeys := []string{
		envMSSQLAllowWrite, envMSSQLAllowDDL, envMSSQLMaxWriteRows,
		envMSSQLWriteTimeout, envMSSQLAuditRedact, envMSSQLAuditTruncateSQL,
	}
	for _, k := range envKeys {
		savedEnv[k] = os.Getenv(k)
		os.Unsetenv(k)
	}
	defer func() {
		for k, v := range savedEnv {
			if v != "" {
				os.Setenv(k, v)
			} else {
				os.Unsetenv(k)
			}
		}
	}()

	// Set test env vars
	for k, v := range env {
		os.Setenv(k, v)
	}

	// Reset and parse flags
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	origCommandLine := flag.CommandLine
	flag.CommandLine = fs
	defer func() { flag.CommandLine = origCommandLine }()

	defineFlags()
	if err := flag.CommandLine.Parse(args); err != nil {
		t.Fatalf("flag parse error: %v", err)
	}

	return buildWriteConfig()
}

func TestBuildWriteConfig_Precedence(t *testing.T) {
	tests := []struct {
		id    string
		args  []string
		env   map[string]string
		check func(t *testing.T, cfg WriteConfig)
	}{
		{
			id:   "TC-PREC-001",
			args: []string{"-allow-write=true"},
			env:  map[string]string{envMSSQLAllowWrite: "false"},
			check: func(t *testing.T, cfg WriteConfig) {
				if !cfg.AllowWrite {
					t.Error("expected AllowWrite=true from flag")
				}
				if cfg.source("AllowWrite") != "flag" {
					t.Errorf("expected source=flag, got %s", cfg.source("AllowWrite"))
				}
			},
		},
		{
			id:   "TC-PREC-002",
			args: []string{},
			env:  map[string]string{envMSSQLAllowWrite: "true"},
			check: func(t *testing.T, cfg WriteConfig) {
				if !cfg.AllowWrite {
					t.Error("expected AllowWrite=true from env")
				}
				if cfg.source("AllowWrite") != "env" {
					t.Errorf("expected source=env, got %s", cfg.source("AllowWrite"))
				}
			},
		},
		{
			id:   "TC-PREC-003",
			args: []string{},
			env:  map[string]string{},
			check: func(t *testing.T, cfg WriteConfig) {
				if cfg.AllowWrite {
					t.Error("expected AllowWrite=false by default")
				}
				if cfg.source("AllowWrite") != "default" {
					t.Errorf("expected source=default, got %s", cfg.source("AllowWrite"))
				}
			},
		},
		{
			id:   "TC-PREC-004",
			args: []string{"-max-write-rows=500"},
			env:  map[string]string{envMSSQLMaxWriteRows: "999"},
			check: func(t *testing.T, cfg WriteConfig) {
				if cfg.MaxRows != 500 {
					t.Errorf("expected MaxRows=500, got %d", cfg.MaxRows)
				}
				if cfg.source("MaxRows") != "flag" {
					t.Errorf("expected source=flag, got %s", cfg.source("MaxRows"))
				}
			},
		},
		{
			id:   "TC-PREC-005",
			args: []string{},
			env:  map[string]string{envMSSQLMaxWriteRows: "200"},
			check: func(t *testing.T, cfg WriteConfig) {
				if cfg.MaxRows != 200 {
					t.Errorf("expected MaxRows=200, got %d", cfg.MaxRows)
				}
				if cfg.source("MaxRows") != "env" {
					t.Errorf("expected source=env, got %s", cfg.source("MaxRows"))
				}
			},
		},
		{
			id:   "TC-PREC-006",
			args: []string{"-write-timeout=10s"},
			env:  map[string]string{envMSSQLWriteTimeout: "2m"},
			check: func(t *testing.T, cfg WriteConfig) {
				if cfg.WriteTimeout != 10*time.Second {
					t.Errorf("expected WriteTimeout=10s, got %v", cfg.WriteTimeout)
				}
				if cfg.source("WriteTimeout") != "flag" {
					t.Errorf("expected source=flag, got %s", cfg.source("WriteTimeout"))
				}
			},
		},
		{
			id:   "TC-PREC-007",
			args: []string{},
			env:  map[string]string{envMSSQLAuditRedact: "true"},
			check: func(t *testing.T, cfg WriteConfig) {
				if !cfg.AuditRedact {
					t.Error("expected AuditRedact=true from env")
				}
				if cfg.source("AuditRedact") != "env" {
					t.Errorf("expected source=env, got %s", cfg.source("AuditRedact"))
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.id, func(t *testing.T) {
			cfg := buildWriteConfigWithFlags(t, tt.args, tt.env)
			tt.check(t, cfg)
		})
	}
}

// TestBuildWriteConfig_MaxRowsZeroFlagPrecedence verifies W1 fix:
// --max-write-rows 0 (explicit flag) takes precedence over env var.
// validateWriteConfig should catch MaxRows=0 as invalid, not the env fallback.
func TestBuildWriteConfig_MaxRowsZeroFlagPrecedence(t *testing.T) {
	cfg := buildWriteConfigWithFlags(t,
		[]string{"-max-write-rows=0"},
		map[string]string{envMSSQLMaxWriteRows: "500"},
	)
	// Flag wins: MaxRows should be 0 (from flag), not 500 (from env)
	if cfg.MaxRows != 0 {
		t.Errorf("W1: expected MaxRows=0 from flag, got %d (env fallback applied)", cfg.MaxRows)
	}
	if cfg.source("MaxRows") != "flag" {
		t.Errorf("W1: expected source=flag, got %s", cfg.source("MaxRows"))
	}
	// validateWriteConfig should catch this
	err := validateWriteConfig(WriteConfig{AllowWrite: true, MaxRows: 0, WriteTimeout: 30 * time.Second})
	if err == nil {
		t.Error("W1: validateWriteConfig should reject MaxRows=0")
	}
}

// TestBuildWriteConfig_InvalidDurationWarning verifies H-001 fix:
// invalid --write-timeout (missing unit) uses default and (conceptually) emits warning.
// We verify the resulting config uses the default value, not the invalid value.
func TestBuildWriteConfig_InvalidDurationWarning(t *testing.T) {
	cfg := buildWriteConfigWithFlags(t,
		[]string{"-write-timeout=30"}, // missing unit suffix — invalid duration
		map[string]string{},
	)
	// Should fall back to 30s default when parse fails
	if cfg.WriteTimeout != 30*time.Second {
		t.Errorf("H-001: expected WriteTimeout=30s (default) for invalid flag, got %v", cfg.WriteTimeout)
	}
	if cfg.source("WriteTimeout") != "default" {
		t.Errorf("H-001: expected source=default for invalid flag, got %s", cfg.source("WriteTimeout"))
	}
}

// TestBuildWriteConfig_InvalidEnvDurationWarning verifies H-001/L-003:
// invalid MSSQL_WRITE_TIMEOUT env var falls back to default.
func TestBuildWriteConfig_InvalidEnvDurationWarning(t *testing.T) {
	cfg := buildWriteConfigWithFlags(t,
		[]string{},
		map[string]string{envMSSQLWriteTimeout: "30"}, // no unit suffix
	)
	if cfg.WriteTimeout != 30*time.Second {
		t.Errorf("L-003: expected WriteTimeout=30s (default) for invalid env, got %v", cfg.WriteTimeout)
	}
	if cfg.source("WriteTimeout") != "default" {
		t.Errorf("L-003: expected source=default for invalid env, got %s", cfg.source("WriteTimeout"))
	}
}
