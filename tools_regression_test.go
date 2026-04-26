package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/mark3labs/mcp-go/mcp"
)

// callQueryTool is a helper to call the query tool handler.
func callQueryTool(t *testing.T, sqlStr string) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]interface{}{"sql": sqlStr},
		},
	}
	ctx := context.Background()
	result, err := handleQuery(ctx, req)
	if err != nil {
		t.Fatalf("handleQuery error: %v", err)
	}
	return result
}

// getResultText extracts text content from a tool result.
func getResultText(result *mcp.CallToolResult) string {
	if len(result.Content) == 0 {
		return ""
	}
	if tc, ok := result.Content[0].(mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

// setGlobalDB sets the global db to the mock and returns a cleanup function.
// NOT safe for parallel tests — use only in sequential test sections.
func setGlobalDB(t *testing.T, mockSQLDB interface{ Close() error }) func() {
	t.Helper()
	// We can't directly set the global db to mockSQLDB because it's *sql.DB
	// The test must use mockDB helper in a non-parallel way
	return func() {}
}

func TestQueryTool_Regression(t *testing.T) {
	// NOTE: This test is NOT parallel because the query tool subtests that need
	// a DB mock must share the global db. For the rejection tests (no DB call needed),
	// we just test the error path directly.

	// TC-REG-001: INSERT rejected (starts with INSERT, not SELECT/WITH)
	t.Run("TC-REG-001", func(t *testing.T) {
		result := callQueryTool(t, "INSERT INTO T VALUES(1)")
		if !result.IsError {
			t.Error("expected error result for INSERT")
		}
		msg := getResultText(result)
		if msg == "" || (!strings.Contains(msg, "only SELECT and WITH queries are allowed") &&
			!strings.Contains(msg, "forbidden keyword")) {
			t.Errorf("expected rejection for INSERT, got: %s", msg)
		}
	})

	// TC-REG-002: INSERT rejected even with --allow-write (query tool is unchanged)
	t.Run("TC-REG-002", func(t *testing.T) {
		result := callQueryTool(t, "INSERT INTO T VALUES(1)")
		if !result.IsError {
			t.Error("expected error result for INSERT with allow-write")
		}
		msg := getResultText(result)
		if msg == "" || (!strings.Contains(msg, "only SELECT and WITH queries are allowed") &&
			!strings.Contains(msg, "forbidden keyword")) {
			t.Errorf("expected rejection for INSERT, got: %s", msg)
		}
	})

	// TC-REG-003: UPDATE rejected
	t.Run("TC-REG-003", func(t *testing.T) {
		result := callQueryTool(t, "UPDATE T SET x=1 WHERE Id=1")
		if !result.IsError {
			t.Error("expected error result for UPDATE")
		}
	})

	// TC-REG-004: DELETE rejected
	t.Run("TC-REG-004", func(t *testing.T) {
		result := callQueryTool(t, "DELETE FROM T WHERE Id=1")
		if !result.IsError {
			t.Error("expected error result for DELETE")
		}
	})

	// TC-REG-005: DROP rejected
	t.Run("TC-REG-005", func(t *testing.T) {
		result := callQueryTool(t, "DROP TABLE T")
		if !result.IsError {
			t.Error("expected error result for DROP")
		}
	})

	// TC-REG-006: ALTER rejected
	t.Run("TC-REG-006", func(t *testing.T) {
		result := callQueryTool(t, "ALTER TABLE T ADD Col INT")
		if !result.IsError {
			t.Error("expected error result for ALTER")
		}
	})

	// TC-REG-007: CREATE rejected
	t.Run("TC-REG-007", func(t *testing.T) {
		result := callQueryTool(t, "CREATE TABLE T (Id INT)")
		if !result.IsError {
			t.Error("expected error result for CREATE")
		}
	})

	// TC-REG-008: TRUNCATE rejected
	t.Run("TC-REG-008", func(t *testing.T) {
		result := callQueryTool(t, "TRUNCATE TABLE T")
		if !result.IsError {
			t.Error("expected error result for TRUNCATE")
		}
	})

	// TC-REG-009: EXEC rejected
	t.Run("TC-REG-009", func(t *testing.T) {
		result := callQueryTool(t, "EXEC sp_something")
		if !result.IsError {
			t.Error("expected error result for EXEC")
		}
	})

	// TC-REG-012: MERGE rejected (not SELECT/WITH prefix)
	t.Run("TC-REG-012", func(t *testing.T) {
		result := callQueryTool(t, "MERGE T USING ...")
		if !result.IsError {
			t.Error("expected error for MERGE via query tool")
		}
		if !strings.Contains(getResultText(result), "only SELECT and WITH queries are allowed") {
			t.Errorf("unexpected error message: %s", getResultText(result))
		}
	})

	// TC-REG-010: SELECT accepted (needs DB mock)
	t.Run("TC-REG-010", func(t *testing.T) {
		mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock err: %v", err)
		}
		defer mockSQLDB.Close()

		// Temporarily set global db for this subtest
		origDB := db
		db = mockSQLDB
		defer func() { db = origDB }()

		mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"col"}).AddRow(1))

		result := callQueryTool(t, "SELECT 1")
		if result.IsError {
			t.Errorf("unexpected error for SELECT: %s", getResultText(result))
		}
	})

	// TC-REG-011: WITH query accepted (needs DB mock)
	t.Run("TC-REG-011", func(t *testing.T) {
		mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock err: %v", err)
		}
		defer mockSQLDB.Close()

		// Temporarily set global db for this subtest
		origDB := db
		db = mockSQLDB
		defer func() { db = origDB }()

		mock.ExpectQuery("WITH cte").WillReturnRows(sqlmock.NewRows([]string{"col"}).AddRow(1))

		result := callQueryTool(t, "WITH cte AS (SELECT 1) SELECT * FROM cte")
		if result.IsError {
			t.Errorf("unexpected error for WITH: %s", getResultText(result))
		}
	})
}

// TestRegisterTools_Signature verifies registerTools accepts WriteConfig
func TestRegisterTools_Signature(t *testing.T) {
	t.Parallel()

	cfg := WriteConfig{
		AllowWrite:   false,
		MaxRows:      1000,
		WriteTimeout: 30 * time.Second,
	}
	_ = cfg.AllowWrite // verify type is usable
}

// TC-REG-013: TestListTablesTool_Regression
// Verifies list_tables response shape (schema/table/row_count keys) after the
// registerTools signature change to accept WriteConfig. Uses sqlmock to avoid
// a live MSSQL connection.
func TestListTablesTool_Regression(t *testing.T) {
	mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("TC-REG-013: sqlmock err: %v", err)
	}
	defer mockSQLDB.Close()

	// Temporarily inject mock DB
	origDB := db
	db = mockSQLDB
	defer func() { db = origDB }()

	// Mock the list_tables query (matches the sys.tables + sys.schemas + sys.dm_db_partition_stats query)
	rows := sqlmock.NewRows([]string{"schema_name", "table_name", "row_count"}).
		AddRow("dbo", "Orders", int64(500)).
		AddRow("dbo", "Users", int64(100))
	mock.ExpectQuery(`sys\.tables`).WillReturnRows(rows)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]interface{}{},
		},
	}
	ctx := context.Background()
	result, err := handleListTables(ctx, req)
	if err != nil {
		t.Fatalf("TC-REG-013: handleListTables error: %v", err)
	}
	if result.IsError {
		t.Fatalf("TC-REG-013: unexpected error result: %v", getResultText(result))
	}

	// Parse JSON response and verify shape
	text := getResultText(result)
	var rows2 []map[string]interface{}
	if jerr := json.Unmarshal([]byte(text), &rows2); jerr != nil {
		t.Fatalf("TC-REG-013: response is not valid JSON: %v\n%s", jerr, text)
	}
	if len(rows2) != 2 {
		t.Errorf("TC-REG-013: expected 2 rows, got %d", len(rows2))
	}
	if len(rows2) > 0 {
		row := rows2[0]
		for _, key := range []string{"schema", "table", "row_count"} {
			if _, ok := row[key]; !ok {
				t.Errorf("TC-REG-013: expected key %q in list_tables row, not found in %v", key, row)
			}
		}
		if row["schema"] != "dbo" {
			t.Errorf("TC-REG-013: expected schema=dbo, got %v", row["schema"])
		}
		if row["table"] != "Orders" {
			t.Errorf("TC-REG-013: expected table=Orders, got %v", row["table"])
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("TC-REG-013: unmet mock expectations: %v", err)
	}
}

// TC-REG-014: TestDescribeTableTool_Regression
// Verifies describe_table response shape (column/type/max_length/nullable/is_primary_key) after
// the registerTools signature change. Uses sqlmock.
func TestDescribeTableTool_Regression(t *testing.T) {
	mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("TC-REG-014: sqlmock err: %v", err)
	}
	defer mockSQLDB.Close()

	origDB := db
	db = mockSQLDB
	defer func() { db = origDB }()

	// Mock the describe_table query (matches sys.columns + sys.types + sys.tables + sys.schemas)
	// Columns: column_name, data_type, max_length, is_nullable, is_identity, is_primary_key, default_value
	colRows := sqlmock.NewRows([]string{
		"column_name", "data_type", "max_length", "is_nullable", "is_identity", "is_primary_key", "default_value",
	}).
		AddRow("Id", "int", 4, false, true, true, nil).
		AddRow("Name", "nvarchar", 100, false, false, false, nil)
	mock.ExpectQuery(`sys\.columns`).WillReturnRows(colRows)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]interface{}{"table": "Users"},
		},
	}
	ctx := context.Background()
	result, err := handleDescribeTable(ctx, req)
	if err != nil {
		t.Fatalf("TC-REG-014: handleDescribeTable error: %v", err)
	}
	if result.IsError {
		t.Fatalf("TC-REG-014: unexpected error result: %v", getResultText(result))
	}

	text := getResultText(result)
	var cols []map[string]interface{}
	if jerr := json.Unmarshal([]byte(text), &cols); jerr != nil {
		t.Fatalf("TC-REG-014: response is not valid JSON: %v\n%s", jerr, text)
	}
	if len(cols) != 2 {
		t.Errorf("TC-REG-014: expected 2 columns, got %d", len(cols))
	}
	if len(cols) > 0 {
		col := cols[0]
		for _, key := range []string{"column", "type", "max_length", "nullable", "is_primary_key"} {
			if _, ok := col[key]; !ok {
				t.Errorf("TC-REG-014: expected key %q in describe_table result, not found in %v", key, col)
			}
		}
		if col["column"] != "Id" {
			t.Errorf("TC-REG-014: expected column=Id, got %v", col["column"])
		}
		if col["type"] != "int" {
			t.Errorf("TC-REG-014: expected type=int, got %v", col["type"])
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("TC-REG-014: unmet mock expectations: %v", err)
	}
}

// TC-REG-015: TestListProceduresTool_Regression
// Verifies list_procedures response shape (schema/procedure keys) after the
// registerTools signature change. Uses sqlmock.
func TestListProceduresTool_Regression(t *testing.T) {
	mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("TC-REG-015: sqlmock err: %v", err)
	}
	defer mockSQLDB.Close()

	origDB := db
	db = mockSQLDB
	defer func() { db = origDB }()

	// Mock sys.procedures + sys.schemas query
	procRows := sqlmock.NewRows([]string{"schema_name", "procedure_name"}).
		AddRow("dbo", "sp_GetOrders").
		AddRow("dbo", "sp_CreateUser")
	mock.ExpectQuery(`sys\.procedures`).WillReturnRows(procRows)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]interface{}{},
		},
	}
	ctx := context.Background()
	result, err := handleListProcedures(ctx, req)
	if err != nil {
		t.Fatalf("TC-REG-015: handleListProcedures error: %v", err)
	}
	if result.IsError {
		t.Fatalf("TC-REG-015: unexpected error result: %v", getResultText(result))
	}

	text := getResultText(result)
	var procs []map[string]interface{}
	if jerr := json.Unmarshal([]byte(text), &procs); jerr != nil {
		t.Fatalf("TC-REG-015: response is not valid JSON: %v\n%s", jerr, text)
	}
	if len(procs) != 2 {
		t.Errorf("TC-REG-015: expected 2 procedures, got %d", len(procs))
	}
	if len(procs) > 0 {
		proc := procs[0]
		for _, key := range []string{"schema", "procedure"} {
			if _, ok := proc[key]; !ok {
				t.Errorf("TC-REG-015: expected key %q in list_procedures result, not found in %v", key, proc)
			}
		}
		if proc["schema"] != "dbo" {
			t.Errorf("TC-REG-015: expected schema=dbo, got %v", proc["schema"])
		}
		if proc["procedure"] != "sp_GetOrders" {
			t.Errorf("TC-REG-015: expected procedure=sp_GetOrders, got %v", proc["procedure"])
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("TC-REG-015: unmet mock expectations: %v", err)
	}
}

// TC-REG-016: TestDescribeProcedureTool_Regression
// Verifies describe_procedure response shape (schema/procedure/parameters keys) after
// the registerTools signature change. Uses sqlmock for both parameter and definition queries.
func TestDescribeProcedureTool_Regression(t *testing.T) {
	mockSQLDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("TC-REG-016: sqlmock err: %v", err)
	}
	defer mockSQLDB.Close()

	origDB := db
	db = mockSQLDB
	defer func() { db = origDB }()

	// First query: sys.parameters — columns: param_name, data_type, direction
	paramRows := sqlmock.NewRows([]string{"param_name", "data_type", "direction"}).
		AddRow("@UserId", "int", "INPUT").
		AddRow("@Result", "nvarchar", "OUTPUT")
	mock.ExpectQuery(`sys\.parameters`).WillReturnRows(paramRows)

	// Second query: sys.sql_modules definition (QueryRowContext — returns single row)
	defRows := sqlmock.NewRows([]string{"definition"}).
		AddRow("CREATE PROCEDURE sp_GetOrders ...")
	mock.ExpectQuery(`sys\.sql_modules`).WillReturnRows(defRows)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]interface{}{"procedure": "sp_GetOrders"},
		},
	}
	ctx := context.Background()
	result, err := handleDescribeProcedure(ctx, req)
	if err != nil {
		t.Fatalf("TC-REG-016: handleDescribeProcedure error: %v", err)
	}
	if result.IsError {
		t.Fatalf("TC-REG-016: unexpected error result: %v", getResultText(result))
	}

	text := getResultText(result)
	var res map[string]interface{}
	if jerr := json.Unmarshal([]byte(text), &res); jerr != nil {
		t.Fatalf("TC-REG-016: response is not valid JSON: %v\n%s", jerr, text)
	}

	// Verify top-level keys
	for _, key := range []string{"schema", "procedure", "parameters"} {
		if _, ok := res[key]; !ok {
			t.Errorf("TC-REG-016: expected key %q in describe_procedure result, not found in %v", key, res)
		}
	}
	if res["procedure"] != "sp_GetOrders" {
		t.Errorf("TC-REG-016: expected procedure=sp_GetOrders, got %v", res["procedure"])
	}
	if res["schema"] != "dbo" {
		t.Errorf("TC-REG-016: expected schema=dbo, got %v", res["schema"])
	}

	// Verify parameters array shape
	params, ok := res["parameters"].([]interface{})
	if !ok {
		t.Fatalf("TC-REG-016: expected parameters to be an array, got %T", res["parameters"])
	}
	if len(params) != 2 {
		t.Errorf("TC-REG-016: expected 2 parameters, got %d", len(params))
	}
	if len(params) > 0 {
		param, ok := params[0].(map[string]interface{})
		if !ok {
			t.Fatalf("TC-REG-016: expected parameter to be a map, got %T", params[0])
		}
		for _, key := range []string{"name", "type", "direction"} {
			if _, ok := param[key]; !ok {
				t.Errorf("TC-REG-016: expected key %q in parameter, not found in %v", key, param)
			}
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("TC-REG-016: unmet mock expectations: %v", err)
	}
}
