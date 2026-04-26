package main

import (
	"fmt"
	"regexp"
	"strings"
)

// StatementVerb identifies the leading verb of a single statement.
type StatementVerb int

const (
	VerbUnknown      StatementVerb = iota
	VerbInsert
	VerbUpdate
	VerbDelete
	VerbMerge
	VerbTruncate     // TRUNCATE TABLE specifically
	VerbCreateTable
	VerbCreateIndex
	VerbAlterTable
	VerbDropTable
	VerbDropIndex
	VerbOtherDDL     // CREATE DATABASE, CREATE LOGIN, GRANT, EXEC, etc.
)

// ClassifiedStatement is the per-statement output of the classifier.
type ClassifiedStatement struct {
	Verb        StatementVerb
	VerbToken   string // e.g. "CREATE TABLE", "DROP DATABASE" — used in error messages
	HasWhere    bool   // true if \bWHERE\b appears in this statement's working copy
	HasOn       bool   // true if \bON\b appears in this statement's working copy
	RawText     string // substring of raw SQL covering this statement (used for audit only)
	OffsetStart int    // byte offset in raw SQL where this statement starts
	OffsetEnd   int    // byte offset where this statement ends
}

// ClassifiedBatch is the full output of the classifier for one execute_write call.
type ClassifiedBatch struct {
	Statements []ClassifiedStatement
	PureDDL    bool // true iff every statement is CreateTable/CreateIndex/AlterTable/DropTable/DropIndex/Truncate
	HasDML     bool // true iff any statement is Insert/Update/Delete/Merge/Truncate
}

// Verb detection regexes — compiled once at package init for performance.
// All are applied to the upper-cased working copy.
var (
	reInsert          = regexp.MustCompile(`^\s*INSERT\b`)
	reUpdate          = regexp.MustCompile(`^\s*UPDATE\b`)
	reDelete          = regexp.MustCompile(`^\s*DELETE\b`)
	reMerge           = regexp.MustCompile(`^\s*MERGE\b`)
	reTruncateTable   = regexp.MustCompile(`^\s*TRUNCATE\s+TABLE\b`)
	reTruncateBare    = regexp.MustCompile(`^\s*TRUNCATE\b`)
	reCreateTable     = regexp.MustCompile(`^\s*CREATE\s+TABLE\b`)
	reCreateIndex     = regexp.MustCompile(`^\s*CREATE\s+(UNIQUE\s+)?(CLUSTERED\s+|NONCLUSTERED\s+)?INDEX\b`)
	reAlterTable      = regexp.MustCompile(`^\s*ALTER\s+TABLE\b`)
	reDropTable       = regexp.MustCompile(`^\s*DROP\s+TABLE\b`)
	reDropIndex       = regexp.MustCompile(`^\s*DROP\s+INDEX\b`)
	// OtherDDL: starts with CREATE|ALTER|DROP|GRANT|REVOKE|DENY|EXEC but doesn't match above
	reOtherDDLCreate  = regexp.MustCompile(`^\s*(CREATE)\s+(\S+)`)
	reOtherDDLAlter   = regexp.MustCompile(`^\s*(ALTER)\s+(\S+)`)
	reOtherDDLDrop    = regexp.MustCompile(`^\s*(DROP)\s+(\S+)`)
	reOtherDDLMisc    = regexp.MustCompile(`^\s*(GRANT|REVOKE|DENY|EXEC)\b`)

	// WHERE and ON detection (case-insensitive word-boundary)
	reWhere = regexp.MustCompile(`(?i)\bWHERE\b`)
	reOn    = regexp.MustCompile(`(?i)\bON\b`)
)

// lexerState represents the current state of the SQL lexer.
type lexerState int

const (
	lsNORMAL           lexerState = iota
	lsIN_STRING
	lsIN_BRACKET_IDENT
	lsIN_LINE_COMMENT
	lsIN_BLOCK_COMMENT
)

// lexSQL runs a single-pass character-by-character lexer over the raw SQL.
// It produces a "working copy" of the same byte length where:
//   - string literal contents are replaced with spaces
//   - bracket-quoted identifier contents are replaced with spaces
//   - line comment contents are replaced with spaces (newline preserved)
//   - block comment contents are replaced with spaces
//   - all delimiters (', [, ], --, /*, */ ) are also replaced with spaces
//   - NORMAL-state characters are copied as-is
//
// Returns an error for unterminated strings, block comments, or bracket identifiers.
func lexSQL(raw string) (string, error) {
	b := []byte(raw)
	out := make([]byte, len(b))
	state := lsNORMAL
	depth := 0
	openOffset := 0

	i := 0
	for i < len(b) {
		c := b[i]

		switch state {
		case lsNORMAL:
			if c == '\'' {
				out[i] = ' '
				state = lsIN_STRING
				openOffset = i
				i++
			} else if c == '[' {
				out[i] = ' '
				state = lsIN_BRACKET_IDENT
				openOffset = i
				i++
			} else if c == '-' && i+1 < len(b) && b[i+1] == '-' {
				out[i] = ' '
				out[i+1] = ' '
				state = lsIN_LINE_COMMENT
				i += 2
			} else if c == '/' && i+1 < len(b) && b[i+1] == '*' {
				out[i] = ' '
				out[i+1] = ' '
				state = lsIN_BLOCK_COMMENT
				depth = 1
				openOffset = i
				i += 2
			} else {
				out[i] = c
				i++
			}

		case lsIN_STRING:
			if c == '\'' {
				if i+1 < len(b) && b[i+1] == '\'' {
					// '' escape: stay in IN_STRING
					out[i] = ' '
					out[i+1] = ' '
					i += 2
				} else {
					// closing quote
					out[i] = ' '
					state = lsNORMAL
					i++
				}
			} else {
				out[i] = ' '
				i++
			}

		case lsIN_BRACKET_IDENT:
			if c == ']' {
				out[i] = ' '
				state = lsNORMAL
				i++
			} else {
				out[i] = ' '
				i++
			}

		case lsIN_LINE_COMMENT:
			if c == '\n' {
				// Preserve newline for offset alignment; return to NORMAL
				out[i] = '\n'
				state = lsNORMAL
				i++
			} else {
				out[i] = ' '
				i++
			}

		case lsIN_BLOCK_COMMENT:
			if c == '/' && i+1 < len(b) && b[i+1] == '*' {
				// Nested block comment — increment depth
				out[i] = ' '
				out[i+1] = ' '
				depth++
				i += 2
			} else if c == '*' && i+1 < len(b) && b[i+1] == '/' {
				// Close block comment
				out[i] = ' '
				out[i+1] = ' '
				depth--
				if depth == 0 {
					state = lsNORMAL
				}
				i += 2
			} else {
				// All other chars (including \n) become spaces
				out[i] = ' '
				i++
			}
		}
	}

	// Check for unterminated sequences
	switch state {
	case lsIN_STRING:
		return "", fmt.Errorf("malformed SQL: unterminated string literal at offset %d", openOffset)
	case lsIN_BLOCK_COMMENT:
		return "", fmt.Errorf("malformed SQL: unterminated block comment at offset %d", openOffset)
	case lsIN_BRACKET_IDENT:
		return "", fmt.Errorf("malformed SQL: unterminated bracket-quoted identifier at offset %d", openOffset)
	}

	return string(out), nil
}

// detectVerb identifies the leading verb of a statement from its upper-cased working copy.
// Returns the verb and a human-readable verb token string.
//
// The regex cascade is ORDER-SENSITIVE: more specific patterns must appear before
// more general ones (e.g. CREATE TABLE before the generic CREATE \S+ fallback).
// Do not reorder cases without updating tests. (S2)
func detectVerb(upperWorking string) (StatementVerb, string) {
	switch {
	case reInsert.MatchString(upperWorking):
		return VerbInsert, "INSERT"
	case reUpdate.MatchString(upperWorking):
		return VerbUpdate, "UPDATE"
	case reDelete.MatchString(upperWorking):
		return VerbDelete, "DELETE"
	case reMerge.MatchString(upperWorking):
		return VerbMerge, "MERGE"
	case reTruncateTable.MatchString(upperWorking):
		return VerbTruncate, "TRUNCATE TABLE"
	case reTruncateBare.MatchString(upperWorking):
		return VerbTruncate, "TRUNCATE"
	case reCreateTable.MatchString(upperWorking):
		return VerbCreateTable, "CREATE TABLE"
	case reCreateIndex.MatchString(upperWorking):
		return VerbCreateIndex, "CREATE INDEX"
	case reAlterTable.MatchString(upperWorking):
		return VerbAlterTable, "ALTER TABLE"
	case reDropTable.MatchString(upperWorking):
		return VerbDropTable, "DROP TABLE"
	case reDropIndex.MatchString(upperWorking):
		return VerbDropIndex, "DROP INDEX"
	default:
		// Check for other DDL verbs
		if m := reOtherDDLCreate.FindStringSubmatch(upperWorking); m != nil {
			return VerbOtherDDL, strings.TrimSpace("CREATE " + m[2])
		}
		if m := reOtherDDLAlter.FindStringSubmatch(upperWorking); m != nil {
			return VerbOtherDDL, strings.TrimSpace("ALTER " + m[2])
		}
		if m := reOtherDDLDrop.FindStringSubmatch(upperWorking); m != nil {
			return VerbOtherDDL, strings.TrimSpace("DROP " + m[2])
		}
		if m := reOtherDDLMisc.FindStringSubmatch(upperWorking); m != nil {
			return VerbOtherDDL, m[1]
		}
		return VerbUnknown, "UNKNOWN"
	}
}

// classifyBatch segments the working copy by semicolons and classifies each statement.
// working is the lexer output (same length as raw); raw is the original SQL.
func classifyBatch(working string, raw string) (ClassifiedBatch, error) {
	var batch ClassifiedBatch

	// Segment by semicolons in the working copy.
	// We walk the working copy to find statement boundaries.
	statements := splitStatements(working, raw)

	if len(statements) == 0 {
		return batch, nil
	}

	batch.Statements = make([]ClassifiedStatement, 0, len(statements))
	batch.HasDML = false

	for _, s := range statements {
		upperWorking := strings.ToUpper(s.working)
		verb, verbToken := detectVerb(upperWorking)

		stmt := ClassifiedStatement{
			Verb:        verb,
			VerbToken:   verbToken,
			HasWhere:    reWhere.MatchString(s.working),
			HasOn:       reOn.MatchString(s.working),
			RawText:     s.raw,
			OffsetStart: s.offsetStart,
			OffsetEnd:   s.offsetEnd,
		}

		batch.Statements = append(batch.Statements, stmt)

		// Track whether any statement is DML/Truncate (for cap and multi-stmt checks).
		switch verb {
		case VerbInsert, VerbUpdate, VerbDelete, VerbMerge, VerbTruncate:
			batch.HasDML = true
		}
	}

	// Compute PureDDL: true only if every statement is whitelisted DDL or TRUNCATE.
	// Done in a single pass here rather than during the loop above to avoid dead writes (W3).
	batch.PureDDL = true
	for _, s := range batch.Statements {
		switch s.Verb {
		case VerbCreateTable, VerbCreateIndex, VerbAlterTable, VerbDropTable, VerbDropIndex, VerbTruncate:
			// OK — whitelisted DDL
		default:
			batch.PureDDL = false
		}
	}

	return batch, nil
}

// statementSlice holds the segmented parts for classification.
type statementSlice struct {
	working     string
	raw         string
	offsetStart int
	offsetEnd   int
}

// splitStatements splits the working copy on semicolons and extracts corresponding
// raw SQL substrings. Empty statements (trailing semicolons, whitespace-only) are dropped.
func splitStatements(working string, raw string) []statementSlice {
	var result []statementSlice

	start := 0
	for i := 0; i <= len(working); i++ {
		isSemi := i < len(working) && working[i] == ';'
		isEnd := i == len(working)

		if isSemi || isEnd {
			workingStmt := working[start:i]
			trimmedWorking := strings.TrimSpace(workingStmt)
			if trimmedWorking != "" {
				// Find the actual non-whitespace start/end within the raw substring
				rawStmt := raw[start:i]
				result = append(result, statementSlice{
					working:     workingStmt,
					raw:         rawStmt,
					offsetStart: start,
					offsetEnd:   i,
				})
			}
			start = i + 1
		}
	}

	return result
}

// policyError carries a rejection reason and human-readable message.
type policyError struct {
	Reason  rejectionReason
	Message string
}

func (e *policyError) Error() string { return e.Message }

// enforcePolicy applies the policy rules to a classified batch.
// Returns nil if the batch is allowed, or a policyError describing the rejection.
func enforcePolicy(batch ClassifiedBatch, cfg WriteConfig, confirm bool, dryRun bool) *policyError {
	// 0. AllowWrite defensive guard
	if !cfg.AllowWrite {
		return &policyError{
			Reason:  reasonToolDisabled,
			Message: "execute_write: execute_write disabled (server started without --allow-write)",
		}
	}

	// 1. Empty batch
	if len(batch.Statements) == 0 {
		return &policyError{
			Reason:  reasonObjectScope,
			Message: "execute_write: no statement provided",
		}
	}

	// 2. Per-statement checks
	for _, s := range batch.Statements {
		// a. Unknown verb
		if s.Verb == VerbUnknown {
			return &policyError{
				Reason:  reasonObjectScope,
				Message: "execute_write: object scope not supported: UNKNOWN requires server- or database-level DDL which is not in scope; only table/index DDL is permitted",
			}
		}

		// b. Other DDL (CREATE DATABASE, GRANT, etc.)
		if s.Verb == VerbOtherDDL {
			return &policyError{
				Reason:  reasonObjectScope,
				Message: fmt.Sprintf("execute_write: object scope not supported: %s requires server- or database-level DDL which is not in scope; only table/index DDL is permitted", s.VerbToken),
			}
		}

		// c. DDL/TRUNCATE without --allow-ddl
		if isDDLVerb(s.Verb) && !cfg.AllowDDL {
			return &policyError{
				Reason:  reasonDDLNotAllowed,
				Message: "execute_write: DDL/TRUNCATE rejected: --allow-ddl not enabled",
			}
		}

		// d. UPDATE/DELETE require WHERE
		if s.Verb == VerbUpdate && !s.HasWhere {
			return &policyError{
				Reason:  reasonWhereMissing,
				Message: "execute_write: WHERE clause required for UPDATE",
			}
		}
		if s.Verb == VerbDelete && !s.HasWhere {
			return &policyError{
				Reason:  reasonWhereMissing,
				Message: "execute_write: WHERE clause required for DELETE",
			}
		}

		// e. MERGE requires ON
		if s.Verb == VerbMerge && !s.HasOn {
			return &policyError{
				Reason:  reasonOnMissing,
				Message: "execute_write: ON clause required for MERGE",
			}
		}
	}

	// 3. Multi-statement guard
	if len(batch.Statements) > 1 && batch.HasDML {
		return &policyError{
			Reason:  reasonMultiStmtDML,
			Message: "execute_write: multi-statement payloads are not allowed for DML",
		}
	}
	if len(batch.Statements) > 1 && !batch.PureDDL {
		return &policyError{
			Reason:  reasonMultiStmtDML,
			Message: "execute_write: multi-statement payloads are not allowed for DML",
		}
	}

	// 4. Confirm guard (skipped if dry_run — D11)
	if !dryRun {
		for _, s := range batch.Statements {
			if s.Verb == VerbTruncate && !confirm {
				return &policyError{
					Reason:  reasonConfirmMissing,
					Message: "execute_write: confirm: true required for TRUNCATE",
				}
			}
			if s.Verb == VerbDropTable && !confirm {
				return &policyError{
					Reason:  reasonConfirmMissing,
					Message: "execute_write: confirm: true required for DROP",
				}
			}
		}
	}

	return nil
}

// isDDLVerb returns true for the whitelisted DDL verbs (including TRUNCATE).
func isDDLVerb(v StatementVerb) bool {
	switch v {
	case VerbCreateTable, VerbCreateIndex, VerbAlterTable, VerbDropTable, VerbDropIndex, VerbTruncate:
		return true
	}
	return false
}

// batchIsAllTruncate returns true if every statement in the batch is a TRUNCATE TABLE.
// Used to exempt pure-TRUNCATE batches from the DML row-cap check (S3: inlined from batchHasOnly).
func batchIsAllTruncate(batch ClassifiedBatch) bool {
	if len(batch.Statements) == 0 {
		return false
	}
	for _, s := range batch.Statements {
		if s.Verb != VerbTruncate {
			return false
		}
	}
	return true
}

// opStringFromBatch returns the audit "op" field value for a batch.
func opStringFromBatch(batch ClassifiedBatch) string {
	if len(batch.Statements) == 0 {
		return ""
	}
	if len(batch.Statements) == 1 {
		return batch.Statements[0].VerbToken
	}
	if batch.PureDDL {
		return "MULTI-DDL"
	}
	return "MULTI-DML"
}
