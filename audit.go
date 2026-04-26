package main

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sync/atomic"
)

// auditEntry is what's serialised to JSON on each invocation.
// Field tags lock the snake_case key order produced by encoding/json for easy parsing.
// Lowercase per Go convention — only used within this package (S4).
type auditEntry struct {
	TS      string `json:"ts"`                // RFC-3339, UTC
	Op      string `json:"op"`                // verb token, e.g. "UPDATE", "CREATE TABLE", "MULTI-DDL"
	Verdict string `json:"verdict"`           // "accepted" | "rejected" | "dry-run" — never ""
	Rows    int64  `json:"rows"`              // user-visible rows from SELECT @@ROWCOUNT; 0 for rejected/dry-run
	DurMS   int64  `json:"dur_ms"`            // wall-clock ms from handler entry to just before this line is written
	Reason  string `json:"reason,omitempty"`  // present on rejected/dry-run only
	SQL     string `json:"sql"`               // raw SQL, possibly redacted/truncated
}

// rejectionReason is an internal enum of audit `reason` tokens (FR-012 vocabulary).
type rejectionReason string

const (
	reasonWhereMissing   rejectionReason = "where-missing"
	reasonOnMissing      rejectionReason = "on-missing"
	reasonConfirmMissing rejectionReason = "confirm-missing"
	reasonMultiStmtDML   rejectionReason = "multi-stmt-dml"
	reasonRowCapExceeded rejectionReason = "row-cap-exceeded"
	reasonWriteTimeout   rejectionReason = "write-timeout"
	reasonClientCancelled rejectionReason = "client-cancelled" // W8: parent context cancelled
	reasonDBError        rejectionReason = "db-error"
	reasonMalformedSQL   rejectionReason = "malformed-sql"
	reasonObjectScope    rejectionReason = "object-scope-unsupported"
	reasonDDLNotAllowed  rejectionReason = "ddl-not-allowed"
	reasonInternal       rejectionReason = "internal-error" // used by panic-default in defer emitAudit
	reasonToolDisabled   rejectionReason = "tool-disabled"  // defensive: tool registered but AllowWrite=false
	// reasonDryRunUnsupported was declared here but never referenced — deleted (W5)
)

// auditFailureCount is incremented atomically whenever writeAuditLine fails.
// flushAuditFailureCount reads this at shutdown.
var auditFailureCount uint64

// writeAuditLine serializes entry to JSON and writes a single line (with trailing newline) to w.
// Returns nil on success; non-nil if marshalling or the write fails.
// Lowercase per Go convention — only used within this package (S4).
func writeAuditLine(w io.Writer, e auditEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// flushAuditFailureCount writes a final summary line to w ONLY if any audit emissions
// failed during the run. Called via defer from main.go.
func flushAuditFailureCount(w io.Writer) {
	n := atomic.LoadUint64(&auditFailureCount)
	if n == 0 {
		return // zero-noise on clean shutdown
	}
	line := fmt.Sprintf(`{"event":"audit_failures","count":%d}`, n)
	_, _ = fmt.Fprintln(w, line) // ignore error: nothing more we can do at shutdown
}

// truncateSQL truncates s to at most max characters, appending "... [truncated]" if truncated.
// max <= 0 means no truncation.
func truncateSQL(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "... [truncated]"
}

// redactorState represents the state of the redactor state machine.
type redactorState int

const (
	rdNORMAL           redactorState = iota
	rdIN_STRING
	rdIN_BRACKET_IDENT
	rdIN_LINE_COMMENT
	rdIN_BLOCK_COMMENT
	rdIN_COMMENT_STRING // sub-state: inside a string literal WITHIN a comment
)

// Regex passes for non-string literals — applied AFTER the state-machine pass.
// Run hex BEFORE numeric so "0x1F2E" is not split into "0" + remaining.
var (
	reHexLiteral     = regexp.MustCompile(`\b0[xX][0-9A-Fa-f]+\b`)
	reNumericLiteral = regexp.MustCompile(`\b[0-9]+(\.[0-9]+)?\b`)
)

// redactLiterals replaces string literals, numeric literals, and hex literals with "?"
// in the SQL string, preserving identifiers, keywords, and comment structure.
// It uses the same state machine alphabet as lexSQL but with different emit rules.
func redactLiterals(raw string) string {
	b := []byte(raw)
	var out []byte
	state := rdNORMAL
	depth := 0

	i := 0
	for i < len(b) {
		c := b[i]

		switch state {
		case rdNORMAL:
			if c == '\'' {
				// Drop the opening quote; enter IN_STRING state
				state = rdIN_STRING
				i++
			} else if c == '[' {
				// Bracket-quoted identifier — copy bracket and contents unchanged
				out = append(out, '[')
				state = rdIN_BRACKET_IDENT
				i++
			} else if c == '-' && i+1 < len(b) && b[i+1] == '-' {
				// Line comment: copy both dashes
				out = append(out, '-', '-')
				state = rdIN_LINE_COMMENT
				i += 2
			} else if c == '/' && i+1 < len(b) && b[i+1] == '*' {
				// Block comment: copy delimiters
				out = append(out, '/', '*')
				state = rdIN_BLOCK_COMMENT
				depth = 1
				i += 2
			} else {
				out = append(out, c)
				i++
			}

		case rdIN_STRING:
			if c == '\'' {
				if i+1 < len(b) && b[i+1] == '\'' {
					// '' escape: drop both (part of the literal being redacted)
					i += 2
				} else {
					// Closing quote: emit single '?' for the whole literal
					out = append(out, '?')
					state = rdNORMAL
					i++
				}
			} else {
				// String content: drop (redacted)
				i++
			}

		case rdIN_BRACKET_IDENT:
			if c == ']' {
				out = append(out, ']')
				state = rdNORMAL
				i++
			} else {
				// Identifier content: copy unchanged
				out = append(out, c)
				i++
			}

		case rdIN_LINE_COMMENT:
			if c == '\n' {
				out = append(out, '\n')
				state = rdNORMAL
				i++
			} else if c == '\'' {
				// Comment-internal string literal: enter sub-state
				// Drop opening quote; will emit '?' on close
				state = rdIN_COMMENT_STRING
				i++
			} else {
				// Comment content: copy as-is (numeric pass will handle numbers)
				out = append(out, c)
				i++
			}

		case rdIN_BLOCK_COMMENT:
			if c == '/' && i+1 < len(b) && b[i+1] == '*' {
				// Nested block comment
				out = append(out, '/', '*')
				depth++
				i += 2
			} else if c == '*' && i+1 < len(b) && b[i+1] == '/' {
				out = append(out, '*', '/')
				depth--
				if depth == 0 {
					state = rdNORMAL
				}
				i += 2
			} else if c == '\'' {
				// Comment-internal string literal sub-state
				state = rdIN_COMMENT_STRING
				i++
			} else {
				// Comment content: copy as-is
				out = append(out, c)
				i++
			}

		case rdIN_COMMENT_STRING:
			// We are inside a string literal that appeared inside a comment.
			// Drop content; emit '?' on close; honor '' escape.
			if c == '\'' {
				if i+1 < len(b) && b[i+1] == '\'' {
					// '' escape: drop both
					i += 2
				} else {
					// Closing quote of comment-internal string
					out = append(out, '?')
					// Return to the appropriate comment state based on depth
					if depth > 0 {
						state = rdIN_BLOCK_COMMENT
					} else {
						state = rdIN_LINE_COMMENT
					}
					i++
				}
			} else if c == '\n' && depth == 0 {
				// End of line comment with unterminated string inside it — treat as line comment end
				out = append(out, '?') // emit the partial literal
				out = append(out, '\n')
				state = rdNORMAL
				i++
			} else {
				// Drop string content
				i++
			}
		}
	}

	// Handle unterminated states gracefully (redactor doesn't error; lexer errors separately)
	if state == rdIN_STRING || state == rdIN_COMMENT_STRING {
		out = append(out, '?')
	}

	// Apply hex literal pass (before numeric, to prevent "0x1F" splitting into "0" + "x1F")
	result := reHexLiteral.ReplaceAllString(string(out), "?")

	// Apply numeric literal pass
	result = reNumericLiteral.ReplaceAllString(result, "?")

	return result
}

// prepareAuditSQL applies redaction and/or truncation to the raw SQL for the audit log.
func prepareAuditSQL(rawSQL string, cfg WriteConfig) string {
	sql := rawSQL
	if cfg.AuditRedact {
		sql = redactLiterals(sql)
	}
	if cfg.AuditTruncateSQL > 0 {
		sql = truncateSQL(sql, cfg.AuditTruncateSQL)
	}
	return sql
}
