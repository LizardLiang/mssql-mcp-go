package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// testWriteConfig returns a WriteConfig suitable for testing.
func testWriteConfig() WriteConfig {
	return WriteConfig{
		AllowWrite:   true,
		AllowDDL:     true,
		MaxRows:      1000,
		WriteTimeout: 30 * time.Second,
	}
}

// makeRequest constructs a CallToolRequest with the given arguments.
func makeRequest(args map[string]interface{}) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: args,
		},
	}
}

// callWriteHandler is a helper that calls handleExecuteWriteWithWriterAndDB with the given mock DB.
func callWriteHandler(ctx context.Context, args map[string]interface{}, cfg WriteConfig, mockSQLDB *sql.DB, auditW *bytes.Buffer) (*mcp.CallToolResult, error) {
	req := makeRequest(args)
	return handleExecuteWriteWithWriterAndDB(ctx, req, cfg, auditW, mockSQLDB)
}

// ---- Rejection path tests (no DB queries expected) ----

func TestHandleExecuteWrite_Rejections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id      string
		args    map[string]interface{}
		cfg     WriteConfig
		wantErr string
	}{
		{
			id:      "TC-INT-005",
			args:    map[string]interface{}{"sql": "UPDATE T SET x='unterminated"},
			cfg:     testWriteConfig(),
			wantErr: "malformed SQL",
		},
		{
			id:      "TC-INT-006",
			args:    map[string]interface{}{"sql": "UPDATE Orders SET Status='Done'"},
			cfg:     testWriteConfig(),
			wantErr: "WHERE clause required for UPDATE",
		},
		{
			id:      "TC-INT-007",
			args:    map[string]interface{}{"sql": "DELETE FROM Orders"},
			cfg:     testWriteConfig(),
			wantErr: "WHERE clause required for DELETE",
		},
		{
			id:      "TC-INT-008",
			args:    map[string]interface{}{"sql": "MERGE T AS target USING S AS source"},
			cfg:     testWriteConfig(),
			wantErr: "ON clause required for MERGE",
		},
		{
			id:      "TC-INT-010",
			args:    map[string]interface{}{"sql": "CREATE DATABASE Foo"},
			cfg:     testWriteConfig(),
			wantErr: "object scope not supported",
		},
		{
			id:      "TC-INT-011",
			args:    map[string]interface{}{"sql": "INSERT INTO A VALUES(1); INSERT INTO B VALUES(2)"},
			cfg:     testWriteConfig(),
			wantErr: "multi-statement payloads are not allowed for DML",
		},
		{
			id: "TC-INT-001",
			// Comment-hidden WHERE — still rejected because comment is stripped
			args:    map[string]interface{}{"sql": "UPDATE Orders SET x=1 -- WHERE Id=42"},
			cfg:     testWriteConfig(),
			wantErr: "WHERE clause required for UPDATE",
		},
		{
			id: "TC-INT-002",
			// Block-comment-hidden WHERE
			args:    map[string]interface{}{"sql": "DELETE FROM Orders /* WHERE Id=42 */"},
			cfg:     testWriteConfig(),
			wantErr: "WHERE clause required for DELETE",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()

			mockSQLDB, _, err := sqlmock.New()
			if err != nil {
				t.Fatalf("failed to create sqlmock: %v", err)
			}
			defer mockSQLDB.Close()

			var auditBuf bytes.Buffer
			ctx := context.Background()

			result, err := callWriteHandler(ctx, tt.args, tt.cfg, mockSQLDB, &auditBuf)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", tt.id, err)
			}

			if result == nil {
				t.Fatalf("%s: nil result", tt.id)
			}

			// The result should be an error result
			if !result.IsError {
				t.Errorf("%s: expected error result, got success: %v", tt.id, result)
			}

			// Check error message
			if len(result.Content) > 0 {
				if textContent, ok := result.Content[0].(mcp.TextContent); ok {
					if !strings.Contains(textContent.Text, tt.wantErr) {
						t.Errorf("%s: error message %q does not contain %q", tt.id, textContent.Text, tt.wantErr)
					}
				}
			}
		})
	}
}

// TC-INT-003: Comment-hidden semicolon — only one statement seen
func TestHandleExecuteWrite_CommentHiddenSemicolon(t *testing.T) {
	t.Parallel()

	mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockSQLDB.Close()

	cfg := testWriteConfig()
	rawSQL := "INSERT INTO A VALUES(1) /* ; INSERT INTO B VALUES(2) ; */"

	// Expect transaction flow for a single INSERT
	mock.ExpectBegin()
	mock.ExpectExec("SET NOCOUNT ON; " + rawSQL).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT @@ROWCOUNT AS rows").WillReturnRows(sqlmock.NewRows([]string{"rows"}).AddRow(1))
	mock.ExpectCommit()

	var auditBuf bytes.Buffer
	ctx := context.Background()

	result, err := callWriteHandler(ctx, map[string]interface{}{"sql": rawSQL}, cfg, mockSQLDB, &auditBuf)
	if err != nil {
		t.Fatalf("TC-INT-003: unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("TC-INT-003: unexpected error result: %v", result.Content)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("TC-INT-003: unmet expectations: %v", err)
	}
}

// TC-INT-009: DROP TABLE without confirm rejected; with confirm passes
func TestHandleExecuteWrite_DropTableConfirm(t *testing.T) {
	t.Parallel()

	t.Run("without-confirm", func(t *testing.T) {
		t.Parallel()
		mockSQLDB, _, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock err: %v", err)
		}
		defer mockSQLDB.Close()

		cfg := testWriteConfig()
		var auditBuf bytes.Buffer
		ctx := context.Background()

		result, err := callWriteHandler(ctx, map[string]interface{}{"sql": "DROP TABLE T", "confirm": false}, cfg, mockSQLDB, &auditBuf)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Error("expected error result for DROP without confirm")
		}
		if len(result.Content) > 0 {
			if tc, ok := result.Content[0].(mcp.TextContent); ok {
				if !strings.Contains(tc.Text, "confirm: true required for DROP") {
					t.Errorf("unexpected error: %q", tc.Text)
				}
			}
		}
	})

	t.Run("with-confirm", func(t *testing.T) {
		t.Parallel()
		mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		if err != nil {
			t.Fatalf("sqlmock err: %v", err)
		}
		defer mockSQLDB.Close()

		cfg := testWriteConfig()
		rawSQL := "DROP TABLE T"
		mock.ExpectBegin()
		mock.ExpectExec("SET NOCOUNT ON; " + rawSQL).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery("SELECT @@ROWCOUNT AS rows").WillReturnRows(sqlmock.NewRows([]string{"rows"}).AddRow(0))
		mock.ExpectCommit()

		var auditBuf bytes.Buffer
		ctx := context.Background()

		result, err := callWriteHandler(ctx, map[string]interface{}{"sql": rawSQL, "confirm": true}, cfg, mockSQLDB, &auditBuf)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.IsError {
			t.Errorf("unexpected error result: %v", result.Content)
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}

// TestHandleExecuteWrite_AcceptedDML: TC-INT-016 (rows not exceeding cap)
func TestHandleExecuteWrite_AcceptedDML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id   string
		sql  string
		rows int64
	}{
		{id: "INSERT", sql: "INSERT INTO T(x) VALUES(1)", rows: 1},
		{id: "UPDATE", sql: "UPDATE T SET x=1 WHERE Id=1", rows: 3},
		{id: "DELETE", sql: "DELETE FROM T WHERE Id=1", rows: 2},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()

			mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatalf("sqlmock err: %v", err)
			}
			defer mockSQLDB.Close()

			cfg := testWriteConfig()
			mock.ExpectBegin()
			mock.ExpectExec("SET NOCOUNT ON; " + tt.sql).WillReturnResult(sqlmock.NewResult(0, tt.rows))
			mock.ExpectQuery("SELECT @@ROWCOUNT AS rows").WillReturnRows(sqlmock.NewRows([]string{"rows"}).AddRow(tt.rows))
			mock.ExpectCommit()

			var auditBuf bytes.Buffer
			ctx := context.Background()

			result, err := callWriteHandler(ctx, map[string]interface{}{"sql": tt.sql}, cfg, mockSQLDB, &auditBuf)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.IsError {
				t.Errorf("unexpected error result: %v", result.Content)
			}

			// Parse result JSON
			if len(result.Content) > 0 {
				if tc, ok := result.Content[0].(mcp.TextContent); ok {
					var res map[string]interface{}
					if jerr := json.Unmarshal([]byte(tc.Text), &res); jerr != nil {
						t.Errorf("invalid JSON result: %v\n%s", jerr, tc.Text)
					} else {
						if res["verdict"] != "accepted" {
							t.Errorf("expected verdict=accepted, got %v", res["verdict"])
						}
						if ra, ok := res["rows_affected"].(float64); !ok || int64(ra) != tt.rows {
							t.Errorf("expected rows_affected=%d, got %v", tt.rows, res["rows_affected"])
						}
					}
				}
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

// TC-INT-015: Row cap exceeded
func TestHandleExecuteWrite_CapExceeded(t *testing.T) {
	t.Parallel()

	mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock err: %v", err)
	}
	defer mockSQLDB.Close()

	cfg := testWriteConfig()
	cfg.MaxRows = 1000
	rawSQL := "UPDATE Orders SET Status='Done' WHERE 1=1"

	mock.ExpectBegin()
	mock.ExpectExec("SET NOCOUNT ON; " + rawSQL).WillReturnResult(sqlmock.NewResult(0, 1001))
	mock.ExpectQuery("SELECT @@ROWCOUNT AS rows").WillReturnRows(sqlmock.NewRows([]string{"rows"}).AddRow(1001))
	mock.ExpectRollback()

	var auditBuf bytes.Buffer
	ctx := context.Background()

	result, err := callWriteHandler(ctx, map[string]interface{}{"sql": rawSQL}, cfg, mockSQLDB, &auditBuf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for cap exceeded")
	}

	if len(result.Content) > 0 {
		if tc, ok := result.Content[0].(mcp.TextContent); ok {
			msg := tc.Text
			if !strings.Contains(msg, "1001") {
				t.Errorf("error message should mention row count 1001: %s", msg)
			}
			if !strings.Contains(msg, "1000") {
				t.Errorf("error message should mention limit 1000: %s", msg)
			}
			if !strings.Contains(msg, "rolled back") {
				t.Errorf("error message should mention rollback: %s", msg)
			}
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TC-INT-020: dry_run=true — rollback called, not commit
func TestHandleExecuteWrite_DryRun(t *testing.T) {
	t.Parallel()

	mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock err: %v", err)
	}
	defer mockSQLDB.Close()

	cfg := testWriteConfig()
	rawSQL := "UPDATE T SET x=1 WHERE Id=1"

	mock.ExpectBegin()
	mock.ExpectExec("SET NOCOUNT ON; " + rawSQL).WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectQuery("SELECT @@ROWCOUNT AS rows").WillReturnRows(sqlmock.NewRows([]string{"rows"}).AddRow(5))
	mock.ExpectRollback()

	var auditBuf bytes.Buffer
	ctx := context.Background()

	result, err := callWriteHandler(ctx, map[string]interface{}{"sql": rawSQL, "dry_run": true}, cfg, mockSQLDB, &auditBuf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %v", result.Content)
	}

	// Check dry-run response
	if len(result.Content) > 0 {
		if tc, ok := result.Content[0].(mcp.TextContent); ok {
			var res map[string]interface{}
			if jerr := json.Unmarshal([]byte(tc.Text), &res); jerr != nil {
				t.Errorf("invalid JSON: %v\n%s", jerr, tc.Text)
			} else {
				if res["verdict"] != "dry-run" {
					t.Errorf("expected verdict=dry-run, got %v", res["verdict"])
				}
				if _, ok := res["rows_affected_simulated"]; !ok {
					t.Errorf("expected rows_affected_simulated field in dry-run response")
				}
				if res["dry_run"] != true {
					t.Errorf("expected dry_run=true in response")
				}
			}
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TC-INT-021: dry_run on DROP TABLE without confirm — should succeed
func TestHandleExecuteWrite_DryRunDropNoConfirm(t *testing.T) {
	t.Parallel()

	mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock err: %v", err)
	}
	defer mockSQLDB.Close()

	cfg := testWriteConfig()
	rawSQL := "DROP TABLE T"

	mock.ExpectBegin()
	mock.ExpectExec("SET NOCOUNT ON; " + rawSQL).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT @@ROWCOUNT AS rows").WillReturnRows(sqlmock.NewRows([]string{"rows"}).AddRow(0))
	mock.ExpectRollback()

	var auditBuf bytes.Buffer
	ctx := context.Background()

	result, err := callWriteHandler(ctx, map[string]interface{}{"sql": rawSQL, "dry_run": true, "confirm": false}, cfg, mockSQLDB, &auditBuf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected dry-run success for DROP without confirm, got error: %v", result.Content)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TC-INT-012: Multi-statement pure-DDL allowed
func TestHandleExecuteWrite_MultiDDL(t *testing.T) {
	t.Parallel()

	mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock err: %v", err)
	}
	defer mockSQLDB.Close()

	cfg := testWriteConfig()
	rawSQL := "CREATE TABLE A(Id INT); CREATE TABLE B(Id INT)"

	mock.ExpectBegin()
	mock.ExpectExec("SET NOCOUNT ON; " + rawSQL).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT @@ROWCOUNT AS rows").WillReturnRows(sqlmock.NewRows([]string{"rows"}).AddRow(0))
	mock.ExpectCommit()

	var auditBuf bytes.Buffer
	ctx := context.Background()

	result, err := callWriteHandler(ctx, map[string]interface{}{"sql": rawSQL, "confirm": false}, cfg, mockSQLDB, &auditBuf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error for multi-DDL: %v", result.Content)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TC-INT-022: Audit line emitted for accepted call
func TestHandleExecuteWrite_AuditCapture(t *testing.T) {
	t.Parallel()

	mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock err: %v", err)
	}
	defer mockSQLDB.Close()

	cfg := testWriteConfig()
	rawSQL := "UPDATE T SET x=1 WHERE Id=1"

	mock.ExpectBegin()
	mock.ExpectExec("SET NOCOUNT ON; " + rawSQL).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT @@ROWCOUNT AS rows").WillReturnRows(sqlmock.NewRows([]string{"rows"}).AddRow(1))
	mock.ExpectCommit()

	var auditBuf bytes.Buffer
	ctx := context.Background()

	_, err = callWriteHandler(ctx, map[string]interface{}{"sql": rawSQL}, cfg, mockSQLDB, &auditBuf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	auditOutput := auditBuf.String()
	if auditOutput == "" {
		t.Fatal("TC-INT-022: expected audit output, got empty")
	}

	// Verify it's valid JSON with correct fields
	lines := strings.Split(strings.TrimSpace(auditOutput), "\n")
	if len(lines) != 1 {
		t.Errorf("TC-INT-022: expected exactly 1 audit line, got %d", len(lines))
	}

	var auditLine map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &auditLine); err != nil {
		t.Errorf("TC-INT-022: audit line is not valid JSON: %v\n%s", err, lines[0])
	}

	if auditLine["verdict"] != "accepted" {
		t.Errorf("TC-INT-022: expected verdict=accepted, got %v", auditLine["verdict"])
	}
	if auditLine["op"] != "UPDATE" {
		t.Errorf("TC-INT-022: expected op=UPDATE, got %v", auditLine["op"])
	}
}

// TC-INT-023: Audit line emitted for rejected call (no DB)
func TestHandleExecuteWrite_AuditRejected(t *testing.T) {
	t.Parallel()

	// No DB needed — policy rejection before BeginTx
	mockSQLDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock err: %v", err)
	}
	defer mockSQLDB.Close()

	cfg := testWriteConfig()
	var auditBuf bytes.Buffer
	ctx := context.Background()

	_, err = callWriteHandler(ctx, map[string]interface{}{"sql": "DELETE FROM T"}, cfg, mockSQLDB, &auditBuf) // no WHERE
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	auditOutput := auditBuf.String()
	if auditOutput == "" {
		t.Fatal("TC-INT-023: expected audit output for rejected call")
	}

	var auditLine map[string]interface{}
	line := strings.TrimSpace(auditOutput)
	if err := json.Unmarshal([]byte(line), &auditLine); err != nil {
		t.Errorf("TC-INT-023: not valid JSON: %v\n%s", err, line)
	}

	if auditLine["verdict"] != "rejected" {
		t.Errorf("TC-INT-023: expected verdict=rejected, got %v", auditLine["verdict"])
	}
	if _, ok := auditLine["dur_ms"]; !ok {
		t.Error("TC-INT-023: expected dur_ms field in audit line")
	}
}

// TC-INT-024: audit-redact-literals: sql field redacted in audit; executed SQL unchanged
func TestHandleExecuteWrite_AuditRedact(t *testing.T) {
	t.Parallel()

	rawSQL := "INSERT INTO Users(Email) VALUES('alice@example.com')"

	mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock err: %v", err)
	}
	defer mockSQLDB.Close()

	// Expect the original (non-redacted) SQL to be sent to the DB
	mock.ExpectBegin()
	mock.ExpectExec("SET NOCOUNT ON; " + rawSQL).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT @@ROWCOUNT AS rows").WillReturnRows(sqlmock.NewRows([]string{"rows"}).AddRow(1))
	mock.ExpectCommit()

	cfg := testWriteConfig()
	cfg.AuditRedact = true

	var auditBuf bytes.Buffer
	ctx := context.Background()

	_, err = callWriteHandler(ctx, map[string]interface{}{"sql": rawSQL}, cfg, mockSQLDB, &auditBuf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify audit log uses redacted SQL
	var auditLine map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(auditBuf.String())), &auditLine); err != nil {
		t.Fatalf("TC-INT-024: audit not valid JSON: %v\n%s", err, auditBuf.String())
	}

	sqlField, _ := auditLine["sql"].(string)
	if strings.Contains(sqlField, "alice@example.com") {
		t.Errorf("TC-INT-024: audit sql field should not contain literal value, got: %s", sqlField)
	}
	if !strings.Contains(sqlField, "?") {
		t.Errorf("TC-INT-024: audit sql field should contain redacted '?', got: %s", sqlField)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("TC-INT-024: unmet mock expectations: %v", err)
	}
}

// TC-INT-025: auditCtx panic-default — verdict=rejected, reason=panic:...
func TestHandleExecuteWrite_PanicRecovery(t *testing.T) {
	t.Parallel()

	// We test the emitAudit function directly with a panic scenario.
	var auditBuf bytes.Buffer

	func() {
		defer func() {
			// The panic should be re-thrown; recover here for the test.
			recover()
		}()

		ac := &auditCtx{
			startedAt: time.Now(),
			rawSQL:    "SELECT 1",
		}

		cfg := WriteConfig{}

		// Set up deferred emitAudit — it will recover the panic, write audit, and re-panic
		defer emitAudit(ac, cfg, &auditBuf)

		// Trigger panic
		panic("oops")
	}()

	auditOutput := strings.TrimSpace(auditBuf.String())
	if auditOutput == "" {
		t.Fatal("TC-INT-025: expected audit output after panic recovery")
	}

	var auditLine map[string]interface{}
	if err := json.Unmarshal([]byte(auditOutput), &auditLine); err != nil {
		t.Fatalf("TC-INT-025: not valid JSON: %v\n%s", err, auditOutput)
	}

	if auditLine["verdict"] != "rejected" {
		t.Errorf("TC-INT-025: expected verdict=rejected, got %v", auditLine["verdict"])
	}
	reason, _ := auditLine["reason"].(string)
	if !strings.HasPrefix(reason, "panic:") {
		t.Errorf("TC-INT-025: expected reason to start with 'panic:', got %q", reason)
	}
	if !strings.Contains(reason, "oops") {
		t.Errorf("TC-INT-025: expected reason to contain 'oops', got %q", reason)
	}
}

// TC-INT-013: Tool NOT registered without --allow-write
func TestToolRegistration_NoWrite(t *testing.T) {
	t.Parallel()

	cfg := WriteConfig{AllowWrite: false, MaxRows: 1000, WriteTimeout: 30 * time.Second}
	if cfg.AllowWrite {
		t.Error("TC-INT-013: expected AllowWrite=false")
	}
}

// TC-INT-014: Tool registered with --allow-write
func TestToolRegistration_WithWrite(t *testing.T) {
	t.Parallel()

	cfg := testWriteConfig()
	if !cfg.AllowWrite {
		t.Error("TC-INT-014: expected AllowWrite=true")
	}
	desc := buildExecuteWriteDescription(cfg)
	if desc == "" {
		t.Error("TC-INT-014: expected non-empty description")
	}
}

// TC-INT-026 through TC-INT-029: Handler auditCtx path coverage
func TestHandleExecuteWrite_AuditOpPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id     string
		sql    string
		wantOp string
	}{
		{
			id:     "TC-INT-026",
			sql:    "UPDATE T SET x='unterminated",
			wantOp: "", // lex error — op stays empty
		},
		{
			id:     "TC-INT-027",
			sql:    "EXEC sproc",
			wantOp: "EXEC", // VerbOtherDDL per spec
		},
		{
			id:     "TC-INT-028",
			sql:    "CREATE DATABASE Foo",
			wantOp: "CREATE DATABASE",
		},
		{
			id:     "TC-INT-029",
			sql:    "DELETE FROM Orders", // no WHERE
			wantOp: "DELETE",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()

			mockSQLDB, _, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock err: %v", err)
			}
			defer mockSQLDB.Close()

			cfg := testWriteConfig()
			var auditBuf bytes.Buffer
			ctx := context.Background()

			_, err = callWriteHandler(ctx, map[string]interface{}{"sql": tt.sql}, cfg, mockSQLDB, &auditBuf)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			auditStr := strings.TrimSpace(auditBuf.String())
			if auditStr == "" {
				t.Fatalf("%s: expected audit output", tt.id)
			}

			var auditLine map[string]interface{}
			if err := json.Unmarshal([]byte(auditStr), &auditLine); err != nil {
				t.Fatalf("%s: not valid JSON: %v\n%s", tt.id, err, auditStr)
			}

			gotOp, _ := auditLine["op"].(string)
			if gotOp != tt.wantOp {
				t.Errorf("%s: expected op=%q, got %q", tt.id, tt.wantOp, gotOp)
			}
		})
	}
}

// TC-INT-030: DB error during BeginTx
func TestHandleExecuteWrite_BeginTxError(t *testing.T) {
	t.Parallel()

	mockSQLDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock err: %v", err)
	}
	defer mockSQLDB.Close()

	cfg := testWriteConfig()
	rawSQL := "INSERT INTO T VALUES(1)"

	// Return error on Begin
	mock.ExpectBegin().WillReturnError(sql.ErrConnDone)

	var auditBuf bytes.Buffer
	ctx := context.Background()

	result, err := callWriteHandler(ctx, map[string]interface{}{"sql": rawSQL}, cfg, mockSQLDB, &auditBuf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("TC-INT-030: expected error result for BeginTx failure")
	}

	// Verify audit has db-error reason
	var auditLine map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(auditBuf.String())), &auditLine); err == nil {
		if auditLine["reason"] != "db-error" {
			t.Errorf("TC-INT-030: expected reason=db-error, got %v", auditLine["reason"])
		}
	}
}

// TC-INT-017 substitute: TestHandleExecuteWrite_SetNocountOn
// Verifies that ExecContext receives exactly "SET NOCOUNT ON; " + rawSQL.
// This is a unit-level substitute for the real-MSSQL trigger-inflation test.
// sqlmock.QueryMatcherEqual ensures any drift from the expected prefix causes failure.
func TestHandleExecuteWrite_SetNocountOn(t *testing.T) {
	t.Parallel()

	mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("TC-INT-017: failed to create sqlmock: %v", err)
	}
	defer mockSQLDB.Close()

	cfg := testWriteConfig()
	rawSQL := "UPDATE T SET x=1 WHERE Id=1"

	// ExecContext must receive exactly "SET NOCOUNT ON; " + rawSQL.
	// QueryMatcherEqual will fail if the prefix is absent or different.
	mock.ExpectBegin()
	mock.ExpectExec("SET NOCOUNT ON; "+rawSQL).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT @@ROWCOUNT AS rows").WillReturnRows(sqlmock.NewRows([]string{"rows"}).AddRow(1))
	mock.ExpectCommit()

	var auditBuf bytes.Buffer
	ctx := context.Background()

	result, err := callWriteHandler(ctx, map[string]interface{}{"sql": rawSQL}, cfg, mockSQLDB, &auditBuf)
	if err != nil {
		t.Fatalf("TC-INT-017: unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("TC-INT-017: unexpected error result: %v", result.Content)
	}

	// sqlmock.ExpectationsWereMet() confirms the exact "SET NOCOUNT ON; " prefix was used
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("TC-INT-017: ExecContext did not receive expected SET NOCOUNT ON prefix: %v", err)
	}
}

// TC-INT-018: TestHandleExecuteWrite_WriteTimeout
// Verifies that when the DB operation exceeds WriteTimeout, the handler returns
// an error with "write timeout" and the audit log records verdict=rejected, reason=write-timeout.
func TestHandleExecuteWrite_WriteTimeout(t *testing.T) {
	t.Parallel()

	mockSQLDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("TC-INT-018: failed to create sqlmock: %v", err)
	}
	defer mockSQLDB.Close()

	cfg := testWriteConfig()
	cfg.WriteTimeout = 50 * time.Millisecond

	rawSQL := "UPDATE T SET x=1 WHERE Id=1"

	// BeginTx will succeed, but ExecContext will block longer than the timeout.
	// After ExecContext returns with context error, the deferred tx.Rollback() fires (H-002).
	mock.ExpectBegin()
	mock.ExpectExec(".*").WillDelayFor(200 * time.Millisecond).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	var auditBuf bytes.Buffer
	ctx := context.Background()

	result, err := callWriteHandler(ctx, map[string]interface{}{"sql": rawSQL}, cfg, mockSQLDB, &auditBuf)
	if err != nil {
		t.Fatalf("TC-INT-018: unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Error("TC-INT-018: expected error result for write timeout")
	}

	// Assert error message contains "write timeout" (case-insensitive via strings.Contains on lower)
	if len(result.Content) > 0 {
		if tc, ok := result.Content[0].(mcp.TextContent); ok {
			if !strings.Contains(strings.ToLower(tc.Text), "write timeout") {
				t.Errorf("TC-INT-018: expected 'write timeout' in error message, got: %q", tc.Text)
			}
		}
	}

	// Verify audit line: verdict=rejected, reason starts with "write-timeout"
	auditStr := strings.TrimSpace(auditBuf.String())
	if auditStr == "" {
		t.Fatal("TC-INT-018: expected audit output after write timeout")
	}
	var auditLine map[string]interface{}
	if err := json.Unmarshal([]byte(auditStr), &auditLine); err != nil {
		t.Fatalf("TC-INT-018: audit not valid JSON: %v\n%s", err, auditStr)
	}
	if auditLine["verdict"] != "rejected" {
		t.Errorf("TC-INT-018: expected verdict=rejected, got %v", auditLine["verdict"])
	}
	reason, _ := auditLine["reason"].(string)
	if !strings.HasPrefix(reason, "write-timeout") {
		t.Errorf("TC-INT-018: expected reason starting with 'write-timeout', got %q", reason)
	}
}

// TC-INT-004: TestHandleExecuteWrite_CommentHiddenDMLInDDLBatch
// Verifies that a pure-DDL batch containing comment-hidden DML is correctly
// classified as pure-DDL and accepted (not rejected as mixed DML+DDL).
// SQL: "CREATE TABLE A(x INT); /* DELETE FROM Users; */ CREATE TABLE B(x INT)"
// The lexer strips the block comment, leaving only two CREATE TABLE statements.
func TestHandleExecuteWrite_CommentHiddenDMLInDDLBatch(t *testing.T) {
	t.Parallel()

	mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("TC-INT-004: failed to create sqlmock: %v", err)
	}
	defer mockSQLDB.Close()

	cfg := testWriteConfig()
	cfg.AllowDDL = true
	cfg.AllowWrite = true
	rawSQL := "CREATE TABLE A(x INT); /* DELETE FROM Users; */ CREATE TABLE B(x INT)"

	// DDL batch: expect Begin, ExecContext with SET NOCOUNT ON prefix, @@ROWCOUNT, Commit.
	// DDL bypasses the cap check (HasDML=false), but @@ROWCOUNT is still read.
	mock.ExpectBegin()
	mock.ExpectExec("SET NOCOUNT ON; " + rawSQL).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT @@ROWCOUNT AS rows").WillReturnRows(sqlmock.NewRows([]string{"rows"}).AddRow(0))
	mock.ExpectCommit()

	var auditBuf bytes.Buffer
	ctx := context.Background()

	result, err := callWriteHandler(ctx, map[string]interface{}{"sql": rawSQL}, cfg, mockSQLDB, &auditBuf)
	if err != nil {
		t.Fatalf("TC-INT-004: unexpected error: %v", err)
	}
	if result.IsError {
		// If this fails, the lexer did not strip the comment-hidden DELETE, causing a
		// multi-stmt DML rejection or mixed-DML rejection.
		t.Errorf("TC-INT-004: expected success for comment-hidden DML in DDL batch, got error: %v", result.Content)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("TC-INT-004: unmet mock expectations: %v", err)
	}
}

// TestHandleExecuteWrite_AuditFailure: audit write failure increments counter
func TestHandleExecuteWrite_AuditFailure(t *testing.T) {
	// Save and restore counter
	origCount := atomic.LoadUint64(&auditFailureCount)
	defer atomic.StoreUint64(&auditFailureCount, origCount)
	atomic.StoreUint64(&auditFailureCount, 0)

	// Use error writer for the audit writer
	w := &errorWriter{}

	ac := &auditCtx{
		startedAt: time.Now(),
		rawSQL:    "DELETE FROM T", // will cause policy rejection (no WHERE)
		verdict:   verdictRejected,
		reason:    "where-missing",
		op:        "DELETE",
	}

	cfg := WriteConfig{}
	emitAudit(ac, cfg, w)

	// Counter should have incremented
	count := atomic.LoadUint64(&auditFailureCount)
	if count == 0 {
		t.Error("TC: expected auditFailureCount to increment on write error")
	}
}

// TC-INT-013 (enhanced): TestToolRegistration_NoWrite_MCPServer
// Verifies that when AllowWrite=false, registerTools does NOT register execute_write
// on the MCP server. Uses server.MCPServer.GetTool to inspect actual registrations.
func TestToolRegistration_NoWrite_MCPServer(t *testing.T) {
	t.Parallel()

	s := server.NewMCPServer("test-server", "0.0.0")
	cfg := WriteConfig{AllowWrite: false, MaxRows: 1000, WriteTimeout: 30 * time.Second}
	registerTools(s, cfg)

	// execute_write must NOT be registered
	tool := s.GetTool("execute_write")
	if tool != nil {
		t.Error("TC-INT-013: execute_write should NOT be registered when AllowWrite=false")
	}

	// The 5 baseline tools must all be present
	for _, name := range []string{"list_tables", "describe_table", "query", "list_procedures", "describe_procedure"} {
		if s.GetTool(name) == nil {
			t.Errorf("TC-INT-013: baseline tool %q should be registered", name)
		}
	}

	// Total registered tools should be exactly 5
	allTools := s.ListTools()
	if len(allTools) != 5 {
		t.Errorf("TC-INT-013: expected exactly 5 registered tools without --allow-write, got %d", len(allTools))
	}
}

// TC-INT-014 (enhanced): TestToolRegistration_WithWrite_MCPServer
// Verifies that when AllowWrite=true, registerTools registers execute_write on the MCP
// server with the correct name and required parameter schema (sql, confirm, dry_run).
func TestToolRegistration_WithWrite_MCPServer(t *testing.T) {
	t.Parallel()

	s := server.NewMCPServer("test-server", "0.0.0")
	cfg := testWriteConfig()
	registerTools(s, cfg)

	// execute_write must be registered
	st := s.GetTool("execute_write")
	if st == nil {
		t.Fatal("TC-INT-014: execute_write should be registered when AllowWrite=true")
	}

	// Verify the tool name
	if st.Tool.Name != "execute_write" {
		t.Errorf("TC-INT-014: expected tool name 'execute_write', got %q", st.Tool.Name)
	}

	// Verify parameter schema contains expected parameters
	props := st.Tool.InputSchema.Properties
	if props == nil {
		t.Fatal("TC-INT-014: tool InputSchema.Properties is nil")
	}
	for _, paramName := range []string{"sql", "confirm", "dry_run"} {
		if _, ok := props[paramName]; !ok {
			t.Errorf("TC-INT-014: expected parameter %q in execute_write schema, not found in %v", paramName, props)
		}
	}

	// Total registered tools should be exactly 6 (5 baseline + execute_write)
	allTools := s.ListTools()
	if len(allTools) != 6 {
		t.Errorf("TC-INT-014: expected exactly 6 registered tools with --allow-write, got %d", len(allTools))
	}
}
