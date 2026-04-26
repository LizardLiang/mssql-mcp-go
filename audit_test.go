package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// errorWriter is a writer that always returns an error.
type errorWriter struct{}

func (e *errorWriter) Write(p []byte) (int, error) {
	return 0, errors.New("write error")
}

// ---- TestRedactLiterals (12 cases) ----

func TestRedactLiterals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id    string
		input string
		want  string
	}{
		{
			id:    "TC-RED-001",
			input: "'O''Brien'",
			want:  "?",
		},
		{
			id:    "TC-RED-002",
			input: "N'DROP TABLE'",
			want:  "N?",
		},
		{
			id:    "TC-RED-003",
			input: "[O'Brien]",
			want:  "[O'Brien]",
		},
		{
			id:    "TC-RED-004",
			input: "Top10Customers WHERE Col1 = 5",
			want:  "Top10Customers WHERE Col1 = ?",
		},
		{
			id:    "TC-RED-005",
			input: "WHERE x = 0x1F2E",
			want:  "WHERE x = ?",
		},
		{
			id:    "TC-RED-006",
			input: "WHERE active = TRUE AND x IS NULL",
			want:  "WHERE active = TRUE AND x IS NULL",
		},
		{
			id:    "TC-RED-007",
			input: "'WHERE 1=1'",
			want:  "?",
		},
		{
			id:    "TC-RED-008",
			input: "-- ID is 42",
			want:  "-- ID is ?",
		},
		{
			id:    "TC-RED-009",
			input: "/* email = 'jane@example.com' */ SELECT 1",
			want:  "/* email = ? */ SELECT ?",
		},
		{
			id:    "TC-RED-010",
			input: "'O''Brien' AND age > 30",
			want:  "? AND age > ?",
		},
		{
			id:    "TC-RED-011",
			input: "INSERT INTO Users(Email, Age) VALUES('alice@example.com', 25)",
			want:  "INSERT INTO Users(Email, Age) VALUES(?, ?)",
		},
		{
			id:    "TC-RED-012",
			input: "UPDATE Users SET PasswordHash='new' WHERE Id=42",
			want:  "UPDATE Users SET PasswordHash=? WHERE Id=?",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()
			got := redactLiterals(tt.input)
			if got != tt.want {
				t.Errorf("%s:\n  input:    %q\n  expected: %q\n  got:      %q", tt.id, tt.input, tt.want, got)
			}
		})
	}
}

// ---- TestTruncateSQL (6 cases) ----

func TestTruncateSQL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id    string
		input string
		max   int
		want  string
	}{
		{
			id:    "TC-TRN-001",
			input: "SELECT 1",
			max:   0,
			want:  "SELECT 1",
		},
		{
			id:    "TC-TRN-002",
			input: "SELECT 1",
			max:   8,
			want:  "SELECT 1",
		},
		{
			id:    "TC-TRN-003",
			input: "SELECT 1",
			max:   7,
			want:  "SELECT ... [truncated]",
		},
		{
			id:    "TC-TRN-004",
			input: "SELECT 1",
			max:   100,
			want:  "SELECT 1",
		},
		{
			id:    "TC-TRN-005",
			input: strings.Repeat("x", 5000),
			max:   200,
			want:  strings.Repeat("x", 200) + "... [truncated]",
		},
		{
			id:    "TC-TRN-006",
			input: "SELECT 1",
			max:   -1,
			want:  "SELECT 1",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()
			got := truncateSQL(tt.input, tt.max)
			if got != tt.want {
				t.Errorf("%s: expected %q, got %q", tt.id, tt.want, got)
			}
		})
	}
}

// ---- TestWriteAuditLine (8 cases) ----

func TestWriteAuditLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id      string
		entry   auditEntry
		wantErr bool
		checks  []func(t *testing.T, output string)
	}{
		{
			id: "TC-AUD-001",
			entry: auditEntry{
				TS:      "2026-04-26T12:00:00Z",
				Op:      "UPDATE",
				Verdict: "accepted",
				Rows:    1,
				DurMS:   23,
				SQL:     "UPDATE Orders SET Status='Done' WHERE Id=42",
			},
			checks: []func(t *testing.T, output string){
				func(t *testing.T, out string) {
					if !strings.Contains(out, `"op":"UPDATE"`) {
						t.Errorf("TC-AUD-001: missing op field, got: %s", out)
					}
					if !strings.Contains(out, `"verdict":"accepted"`) {
						t.Errorf("TC-AUD-001: missing verdict field, got: %s", out)
					}
					if !strings.Contains(out, `"rows":1`) {
						t.Errorf("TC-AUD-001: missing rows field, got: %s", out)
					}
					if !strings.Contains(out, `"dur_ms":23`) {
						t.Errorf("TC-AUD-001: missing dur_ms field, got: %s", out)
					}
				},
			},
		},
		{
			id: "TC-AUD-002",
			entry: auditEntry{
				TS:      "2026-04-26T12:00:01Z",
				Op:      "DELETE",
				Verdict: "rejected",
				Reason:  "where-missing",
				DurMS:   1,
				SQL:     "DELETE FROM Orders",
			},
			checks: []func(t *testing.T, output string){
				func(t *testing.T, out string) {
					if !strings.Contains(out, `"reason":"where-missing"`) {
						t.Errorf("TC-AUD-002: missing reason field, got: %s", out)
					}
					if !strings.Contains(out, `"verdict":"rejected"`) {
						t.Errorf("TC-AUD-002: missing verdict, got: %s", out)
					}
				},
			},
		},
		{
			id: "TC-AUD-003",
			entry: auditEntry{
				TS:      "2026-04-26T12:00:02Z",
				Op:      "DROP TABLE",
				Verdict: "dry-run",
				DurMS:   18,
				SQL:     "DROP TABLE T",
			},
			checks: []func(t *testing.T, output string){
				func(t *testing.T, out string) {
					if !strings.Contains(out, `"verdict":"dry-run"`) {
						t.Errorf("TC-AUD-003: missing dry-run verdict, got: %s", out)
					}
				},
			},
		},
		{
			id: "TC-AUD-004",
			entry: auditEntry{
				TS:      "2026-04-26T12:00:03Z",
				Op:      "INSERT",
				Verdict: "accepted",
				Reason:  "", // should be omitted by omitempty
				DurMS:   5,
				SQL:     "INSERT INTO T VALUES(1)",
			},
			checks: []func(t *testing.T, output string){
				func(t *testing.T, out string) {
					if strings.Contains(out, `"reason"`) {
						t.Errorf("TC-AUD-004: reason should be absent (omitempty), got: %s", out)
					}
				},
			},
		},
		{
			id:      "TC-AUD-005",
			entry:   auditEntry{TS: "2026-04-26T12:00:00Z", Verdict: "accepted", SQL: "SELECT 1"},
			wantErr: true,
		},
		{
			id: "TC-AUD-006",
			entry: auditEntry{
				TS:      "2026-04-26T12:00:00Z",
				Verdict: "accepted",
				SQL:     "SELECT 1",
			},
			checks: []func(t *testing.T, output string){
				func(t *testing.T, out string) {
					if !strings.HasSuffix(out, "\n") {
						t.Errorf("TC-AUD-006: output does not end with newline: %q", out)
					}
				},
			},
		},
		{
			id: "TC-AUD-007",
			entry: auditEntry{
				TS:      "2026-04-26T12:00:00Z",
				Verdict: "accepted",
				SQL:     `SELECT "col" FROM T` + "\nline2",
			},
			checks: []func(t *testing.T, output string){
				func(t *testing.T, out string) {
					// Verify it's valid JSON
					var m map[string]interface{}
					line := strings.TrimSpace(out)
					if err := json.Unmarshal([]byte(line), &m); err != nil {
						t.Errorf("TC-AUD-007: output is not valid JSON: %v\nOutput: %s", err, out)
					}
				},
			},
		},
		{
			id: "TC-AUD-008",
			entry: auditEntry{
				TS:      "2026-04-26T12:00:00Z",
				Op:      "UPDATE",
				Verdict: "rejected",
				Reason:  "where-missing",
				DurMS:   0,
				SQL:     "UPDATE T SET x=1",
			},
			checks: []func(t *testing.T, output string){
				func(t *testing.T, out string) {
					if !strings.Contains(out, `"dur_ms":0`) {
						t.Errorf("TC-AUD-008: missing dur_ms:0, got: %s", out)
					}
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()

			if tt.wantErr {
				// TC-AUD-005: use error-returning writer
				w := &errorWriter{}
				err := writeAuditLine(w, tt.entry)
				if err == nil {
					t.Errorf("%s: expected error, got nil", tt.id)
				}
				return
			}

			var buf bytes.Buffer
			err := writeAuditLine(&buf, tt.entry)
			if err != nil {
				t.Errorf("%s: unexpected error: %v", tt.id, err)
				return
			}

			out := buf.String()
			for _, check := range tt.checks {
				check(t, out)
			}
		})
	}
}

// ---- TestFlushAuditFailureCount (3 cases) ----

func TestFlushAuditFailureCount(t *testing.T) {
	// Save and restore the global counter
	orig := atomic.LoadUint64(&auditFailureCount)
	defer atomic.StoreUint64(&auditFailureCount, orig)

	t.Run("TC-AFC-001", func(t *testing.T) {
		atomic.StoreUint64(&auditFailureCount, 0)
		var buf bytes.Buffer
		flushAuditFailureCount(&buf)
		if buf.Len() > 0 {
			t.Errorf("TC-AFC-001: expected no output for count=0, got: %q", buf.String())
		}
	})

	t.Run("TC-AFC-002", func(t *testing.T) {
		atomic.StoreUint64(&auditFailureCount, 5)
		var buf bytes.Buffer
		flushAuditFailureCount(&buf)
		out := strings.TrimSpace(buf.String())
		want := `{"event":"audit_failures","count":5}`
		if out != want {
			t.Errorf("TC-AFC-002:\n  expected: %q\n  got:      %q", want, out)
		}
	})

	t.Run("TC-AFC-003", func(t *testing.T) {
		atomic.StoreUint64(&auditFailureCount, 0)
		var buf bytes.Buffer
		flushAuditFailureCount(&buf)
		if buf.Len() > 0 {
			t.Errorf("TC-AFC-003: expected no output on second call with count=0, got: %q", buf.String())
		}
		// Second call still produces nothing
		flushAuditFailureCount(&buf)
		if buf.Len() > 0 {
			t.Errorf("TC-AFC-003: expected no output on repeated calls with count=0, got: %q", buf.String())
		}
	})
}
