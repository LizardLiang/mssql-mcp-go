package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func getArgs(request mcp.CallToolRequest) map[string]interface{} {
	if m, ok := request.Params.Arguments.(map[string]interface{}); ok {
		return m
	}
	return map[string]interface{}{}
}

// stripQuotedSegments removes bracket-quoted identifiers ([name]) and single-quoted
// string literals ('value') from SQL so the forbidden-keyword scanner doesn't fire
// on column names or data values that happen to spell out a keyword.
var (
	reBracketIdent  = regexp.MustCompile(`\[[^\]]*\]`)
	reSingleQuoted  = regexp.MustCompile(`'[^']*(?:''[^']*)*'`)
	reDoubleQuoted  = regexp.MustCompile(`"[^"]*(?:""[^"]*)*"`)
)

func stripQuotedSegments(sql string) string {
	sql = reSingleQuoted.ReplaceAllString(sql, "''")
	sql = reDoubleQuoted.ReplaceAllString(sql, `""`)
	sql = reBracketIdent.ReplaceAllString(sql, "[x]")
	return sql
}

func handleListTables(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	schema, _ := getArgs(request)["schema"].(string)

	query := `
		SELECT s.name AS schema_name, t.name AS table_name, SUM(p.row_count) AS row_count
		FROM sys.tables t
		JOIN sys.schemas s ON t.schema_id = s.schema_id
		JOIN sys.dm_db_partition_stats p ON t.object_id = p.object_id AND p.index_id IN (0, 1)
		WHERE 1=1`

	args := []interface{}{}
	if schema != "" {
		query += " AND s.name = @p1"
		args = append(args, schema)
	}

	query += " GROUP BY s.name, t.name ORDER BY s.name, t.name"

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("query failed: %v", err)), nil
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var schemaName, tableName string
		var rowCount int64
		if err := rows.Scan(&schemaName, &tableName, &rowCount); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("scan failed: %v", err)), nil
		}

		results = append(results, map[string]interface{}{
			"schema":    schemaName,
			"table":     tableName,
			"row_count": rowCount,
		})
	}

	out, _ := json.MarshalIndent(results, "", " ")
	return mcp.NewToolResultText(string(out)), nil
}

func handleDescribeTable(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	table, _ := getArgs(request)["table"].(string)
	if table == "" {
		return mcp.NewToolResultError("table parameter is required"), nil
	}

	schema, _ := getArgs(request)["schema"].(string)
	if schema == "" {
		schema = "dbo"
	}

	query := `
		SELECT
			c.name AS column_name,
			tp.name AS data_type,
			c.max_length,
			c.is_nullable,
			c.is_identity,
			CASE WHEN EXISTS (
				SELECT 1 FROM sys.index_columns ic
				JOIN sys.indexes i ON ic.object_id = i.object_id AND ic.index_id = i.index_id
				WHERE i.is_primary_key = 1 AND ic.object_id = c.object_id AND ic.column_id = c.column_id
			) THEN 1 ELSE 0 END AS is_primary_key,
			dc.definition AS default_value
		FROM sys.columns c
		JOIN sys.types tp ON c.user_type_id = tp.user_type_id
		JOIN sys.tables t ON c.object_id = t.object_id
		JOIN sys.schemas s ON t.schema_id = s.schema_id
		LEFT JOIN sys.default_constraints dc ON dc.parent_object_id = t.object_id AND dc.parent_column_id = c.column_id
		WHERE t.name = @p1 AND s.name = @p2
		ORDER BY c.column_id`

	rows, err := db.QueryContext(ctx, query, table, schema)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("query failed: %v", err)), nil
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var colName, dataType string
		var maxLen int
		var nullable, identity, pk bool
		var defaultVal *string
		if err := rows.Scan(&colName, &dataType, &maxLen, &nullable, &identity, &pk, &defaultVal); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("scan failed: %v", err)), nil
		}
		row := map[string]interface{}{
			"column":         colName,
			"type":           dataType,
			"max_length":     maxLen,
			"nullable":       nullable,
			"is_identity":    identity,
			"is_primary_key": pk,
		}
		if defaultVal != nil {
			row["default"] = *defaultVal
		}
		results = append(results, row)
	}

	out, _ := json.MarshalIndent(results, "", " ")
	return mcp.NewToolResultText(string(out)), nil
}

func handleQuery(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sqlStr, _ := getArgs(request)["sql"].(string)
	if sqlStr == "" {
		return mcp.NewToolResultError("sql parameter is required"), nil
	}

	upper := strings.ToUpper(strings.TrimSpace(sqlStr))
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return mcp.NewToolResultError("only SELECT and WITH queries are allowed"), nil
	}

	// Strip bracket-quoted identifiers ([delete], [update], etc.) and single-quoted
	// string literals before scanning for forbidden keywords, so that column names
	// and string values that happen to match a keyword don't produce false positives.
	scanTarget := stripQuotedSegments(upper)

	forbidden := []string{"INSERT", "UPDATE", "DELETE", "DROP", "ALTER", "CREATE", "TRUNCATE", "EXEC"}

	for _, kw := range forbidden {
		pattern := `\b` + kw + `\b`
		if matched, _ := regexp.MatchString(pattern, scanTarget); matched {
			return mcp.NewToolResultError(fmt.Sprintf("forbidden keyword: %s", kw)), nil
		}
	}

	rows, err := db.QueryContext(ctx, sqlStr)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("query failed: %v", err)), nil
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	var results []map[string]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("scan failed: %v", err)), nil
		}

		row := make(map[string]interface{})
		for i, col := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			row[col] = v
		}

		results = append(results, row)
	}

	out, _ := json.MarshalIndent(results, "", " ")
	return mcp.NewToolResultText(string(out)), nil
}

func handleListProcedures(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	schema, _ := getArgs(request)["schema"].(string)

	query := `
		SELECT s.name AS schema_name, p.name AS procedure_name
		FROM sys.procedures p
		JOIN sys.schemas s ON p.schema_id = s.schema_id
		WHERE 1=1`

	args := []interface{}{}
	if schema != "" {
		query += " AND s.name = @p1"
		args = append(args, schema)
	}

	query += " ORDER BY s.name, p.name"

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("query failed: %v", err)), nil
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var schemaName, procName string
		if err := rows.Scan(&schemaName, &procName); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("scan failed: %v", err)), nil
		}
		results = append(results, map[string]interface{}{
			"schema":    schemaName,
			"procedure": procName,
		})
	}

	out, _ := json.MarshalIndent(results, "", " ")
	return mcp.NewToolResultText(string(out)), nil
}

func handleDescribeProcedure(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	proc, _ := getArgs(request)["procedure"].(string)
	if proc == "" {
		return mcp.NewToolResultError("procedure parameter is required"), nil
	}

	schema, _ := getArgs(request)["schema"].(string)
	if schema == "" {
		schema = "dbo"
	}

	paramQuery := `
		SELECT
			p.name AS param_name,
			t.name AS data_type,
			CASE WHEN p.is_output = 1 THEN 'OUTPUT' ELSE 'INPUT' END AS direction
		FROM sys.parameters p
		JOIN sys.types t ON p.user_type_id = t.user_type_id
		JOIN sys.procedures pr ON p.object_id = pr.object_id
		JOIN sys.schemas s ON pr.schema_id = s.schema_id
		WHERE pr.name = @p1 AND s.name = @p2
		ORDER BY p.parameter_id`

	rows, err := db.QueryContext(ctx, paramQuery, proc, schema)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("query failed: %v", err)), nil
	}
	defer rows.Close()

	var params []map[string]interface{}
	for rows.Next() {
		var name, dataType, direction string
		if err := rows.Scan(&name, &dataType, &direction); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("scan failed: %v", err)), nil
		}
		params = append(params, map[string]interface{}{
			"name":      name,
			"type":      dataType,
			"direction": direction,
		})
	}

	defQuery := `
		SELECT m.definition
		FROM sys.sql_modules m
		JOIN sys.procedures p ON m.object_id = p.object_id
		JOIN sys.schemas s ON p.schema_id = s.schema_id
		WHERE p.name = @p1 AND s.name = @p2`

	var definition *string
	db.QueryRowContext(ctx, defQuery, proc, schema).Scan(&definition)

	result := map[string]interface{}{
		"schema":     schema,
		"procedure":  proc,
		"parameters": params,
	}
	if definition != nil {
		result["definition"] = *definition
	}

	out, _ := json.MarshalIndent(result, "", " ")
	return mcp.NewToolResultText(string(out)), nil
}

func registerTools(s *server.MCPServer, cfg WriteConfig) {
	s.AddTool(mcp.NewTool("list_tables",
		mcp.WithDescription("List all tables in the database with schema name and row counts"),
		mcp.WithString("schema", mcp.Description("Filter by schema name")),
	), handleListTables)

	s.AddTool(mcp.NewTool("describe_table",
		mcp.WithDescription("Get column details for a specific table"),
		mcp.WithString("table", mcp.Required(), mcp.Description("Table name")),
		mcp.WithString("schema", mcp.Description("Schema name (default: dbo)")),
	), handleDescribeTable)

	s.AddTool(mcp.NewTool("query",
		mcp.WithDescription("Execute a read-only SQL SELECT query"),
		mcp.WithString("sql", mcp.Required(), mcp.Description("SQL SELECT query to execute")),
	), handleQuery)

	s.AddTool(mcp.NewTool("list_procedures",
		mcp.WithDescription("List all stored procedures"),
		mcp.WithString("schema", mcp.Description("Filter by schema name")),
	), handleListProcedures)

	s.AddTool(mcp.NewTool("describe_procedure",
		mcp.WithDescription("Get stored procedure parameters and source code"),
		mcp.WithString("procedure", mcp.Required(), mcp.Description("Procedure name")),
		mcp.WithString("schema", mcp.Description("Schema name (default: dbo)")),
	), handleDescribeProcedure)

	// Conditionally register the write tool (FR-002)
	if cfg.AllowWrite {
		registerWriteTool(s, cfg)
	}
}
