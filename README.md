# mssql-mcp

A read-only MCP (Model Context Protocol) server for SQL Server. Connects to a MSSQL database and exposes tools for querying data, inspecting table schemas, and viewing stored procedure details via stdio transport.

## Tools

| Tool | Description | Parameters |
|------|-------------|------------|
| `list_tables` | List all tables with schema and row counts | `schema` (optional) |
| `describe_table` | Get column details for a table | `table` (required), `schema` (optional, default: `dbo`) |
| `query` | Execute a read-only SELECT query | `sql` (required) |
| `list_procedures` | List all stored procedures | `schema` (optional) |
| `describe_procedure` | Get procedure parameters and source code | `procedure` (required), `schema` (optional, default: `dbo`) |

The `query` tool only allows `SELECT` and `WITH` statements. `INSERT`, `UPDATE`, `DELETE`, `DROP`, `ALTER`, `CREATE`, `TRUNCATE`, and `EXEC` are blocked.

## Build

```bash
go mod tidy
go build -o mssql-mcp
```

## Configuration

Set the connection string in `run.sh`:

```bash
export MSSQL_CONNECTION_STRING="sqlserver://user:pass@host:port?database=mydb"
```

## Claude Code Integration

Add to `.claude.json` under the target project's `mcpServers`:

```json
{
  "mcpServers": {
    "mssql": {
      "command": "/path/to/mssql-mcp/run.sh",
      "args": []
    }
  }
}
```

---

# mssql-mcp (繁體中文)

一個唯讀的 MCP (Model Context Protocol) 伺服器，用於連接 SQL Server。透過 stdio 傳輸協定，提供查詢資料、檢視資料表結構及預存程序詳細資訊的工具。

## 工具

| 工具 | 說明 | 參數 |
|------|------|------|
| `list_tables` | 列出所有資料表及其 schema 與列數 | `schema`（選填） |
| `describe_table` | 取得資料表的欄位詳細資訊 | `table`（必填）、`schema`（選填，預設：`dbo`） |
| `query` | 執行唯讀的 SELECT 查詢 | `sql`（必填） |
| `list_procedures` | 列出所有預存程序 | `schema`（選填） |
| `describe_procedure` | 取得預存程序的參數與原始碼 | `procedure`（必填）、`schema`（選填，預設：`dbo`） |

`query` 工具僅允許 `SELECT` 和 `WITH` 語句。`INSERT`、`UPDATE`、`DELETE`、`DROP`、`ALTER`、`CREATE`、`TRUNCATE` 及 `EXEC` 皆被禁止。

## 建置

```bash
go mod tidy
go build -o mssql-mcp
```

## 設定

在 `run.sh` 中設定連線字串：

```bash
export MSSQL_CONNECTION_STRING="sqlserver://user:pass@host:port?database=mydb"
```

## Claude Code 整合

在目標專案的 `.claude.json` 中的 `mcpServers` 加入：

```json
{
  "mcpServers": {
    "mssql": {
      "command": "/path/to/mssql-mcp/run.sh",
      "args": []
    }
  }
}
```
