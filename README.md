# mssql-mcp

An MCP (Model Context Protocol) server for SQL Server. Connects to a MSSQL database and exposes tools for querying data, inspecting table schemas, and viewing stored procedure details via stdio transport. Read-only by default; write operations are opt-in via flags.

## Tools

| Tool | Description | Parameters |
|------|-------------|------------|
| `list_tables` | List all tables with schema and row counts | `schema` (optional) |
| `describe_table` | Get column details for a table | `table` (required), `schema` (optional, default: `dbo`) |
| `query` | Execute a read-only SELECT query | `sql` (required) |
| `list_procedures` | List all stored procedures | `schema` (optional) |
| `describe_procedure` | Get procedure parameters and source code | `procedure` (required), `schema` (optional, default: `dbo`) |
| `execute_write` | Execute a write statement (DML/DDL) | `sql` (required), `confirm` (optional), `dry_run` (optional) |

The `query` tool only allows `SELECT` and `WITH` statements. `INSERT`, `UPDATE`, `DELETE`, `DROP`, `ALTER`, `CREATE`, `TRUNCATE`, and `EXEC` are blocked.

The `execute_write` tool is hidden and unavailable unless `--allow-write` is set.

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

## Write Mode

Write operations are disabled by default. Use the following flags (or equivalent environment variables) to enable them:

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--allow-write` | `MSSQL_ALLOW_WRITE` | `false` | Enable `execute_write` for DML (INSERT/UPDATE/DELETE/MERGE) |
| `--allow-ddl` | `MSSQL_ALLOW_DDL` | `false` | Also allow whitelisted DDL (CREATE/ALTER/DROP TABLE/INDEX, TRUNCATE TABLE); requires `--allow-write` |
| `--max-write-rows N` | `MSSQL_MAX_WRITE_ROWS` | `1000` | Maximum rows affected per DML statement; exceeding this rolls back the transaction |
| `--write-timeout dur` | `MSSQL_WRITE_TIMEOUT` | `30s` | Per-statement timeout (e.g. `60s`, `5m`) |
| `--audit-redact-literals` | `MSSQL_AUDIT_REDACT_LITERALS` | `false` | Replace string/numeric literals with `?` in the audit log |
| `--audit-truncate-sql N` | `MSSQL_AUDIT_TRUNCATE_SQL` | `0` (no truncation) | Truncate the SQL field in audit log entries to N characters |

CLI flags take precedence over environment variables.

### Safety rails

- `UPDATE` and `DELETE` without a `WHERE` clause are rejected.
- `MERGE` without an `ON` clause is rejected.
- `TRUNCATE TABLE` and `DROP` require `confirm: true` in the tool call (unless `dry_run: true`).
- DDL is limited to `CREATE/ALTER/DROP TABLE`, `CREATE/DROP INDEX`, and `TRUNCATE TABLE`. Database-level and server-level DDL is blocked.
- Multi-statement payloads are rejected unless all statements are DDL.
- Comment and string-literal stripping prevents comment-hidden DML from bypassing keyword checks.
- Every attempt is written as a single-line JSON audit entry to stderr (verdict, op, rows, duration, redacted SQL).

### `execute_write` parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `sql` | string (required) | SQL statement to execute |
| `confirm` | boolean | Set `true` to authorize `TRUNCATE`/`DROP`; required for those verbs unless `dry_run` is set |
| `dry_run` | boolean | Runs inside a transaction that always rolls back and reports would-be row count; overrides the `confirm` requirement |

### Example — enabling writes in Claude Code

```json
{
  "mcpServers": {
    "mssql": {
      "command": "/path/to/mssql-mcp/run.sh",
      "args": ["--allow-write", "--max-write-rows", "500"]
    }
  }
}
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

一個 MCP (Model Context Protocol) 伺服器，用於連接 SQL Server。透過 stdio 傳輸協定，提供查詢資料、檢視資料表結構及預存程序詳細資訊的工具。預設為唯讀模式；寫入操作需透過旗標明確啟用。

## 工具

| 工具 | 說明 | 參數 |
|------|------|------|
| `list_tables` | 列出所有資料表及其 schema 與列數 | `schema`（選填） |
| `describe_table` | 取得資料表的欄位詳細資訊 | `table`（必填）、`schema`（選填，預設：`dbo`） |
| `query` | 執行唯讀的 SELECT 查詢 | `sql`（必填） |
| `list_procedures` | 列出所有預存程序 | `schema`（選填） |
| `describe_procedure` | 取得預存程序的參數與原始碼 | `procedure`（必填）、`schema`（選填，預設：`dbo`） |
| `execute_write` | 執行寫入語句（DML/DDL） | `sql`（必填）、`confirm`（選填）、`dry_run`（選填） |

`query` 工具僅允許 `SELECT` 和 `WITH` 語句。`INSERT`、`UPDATE`、`DELETE`、`DROP`、`ALTER`、`CREATE`、`TRUNCATE` 及 `EXEC` 皆被禁止。

`execute_write` 工具預設隱藏，必須設定 `--allow-write` 才會啟用。

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

## 寫入模式

寫入操作預設停用。使用下列旗標（或對應的環境變數）來啟用：

| 旗標 | 環境變數 | 預設值 | 說明 |
|------|----------|--------|------|
| `--allow-write` | `MSSQL_ALLOW_WRITE` | `false` | 啟用 `execute_write` 進行 DML（INSERT/UPDATE/DELETE/MERGE） |
| `--allow-ddl` | `MSSQL_ALLOW_DDL` | `false` | 同時允許白名單 DDL（CREATE/ALTER/DROP TABLE/INDEX、TRUNCATE TABLE）；需搭配 `--allow-write` |
| `--max-write-rows N` | `MSSQL_MAX_WRITE_ROWS` | `1000` | 每次 DML 最多影響的列數；超過則回滾交易 |
| `--write-timeout dur` | `MSSQL_WRITE_TIMEOUT` | `30s` | 每次語句的逾時時間（例如 `60s`、`5m`） |
| `--audit-redact-literals` | `MSSQL_AUDIT_REDACT_LITERALS` | `false` | 在稽核日誌中將字串/數值字面值替換為 `?` |
| `--audit-truncate-sql N` | `MSSQL_AUDIT_TRUNCATE_SQL` | `0`（不截斷） | 將稽核日誌中的 SQL 欄位截斷至 N 個字元 |

CLI 旗標優先於環境變數。

### 安全防護

- 未含 `WHERE` 子句的 `UPDATE` 與 `DELETE` 會被拒絕。
- 未含 `ON` 子句的 `MERGE` 會被拒絕。
- `TRUNCATE TABLE` 與 `DROP` 需在工具呼叫中傳入 `confirm: true`（除非使用 `dry_run: true`）。
- DDL 僅限於 `CREATE/ALTER/DROP TABLE`、`CREATE/DROP INDEX` 及 `TRUNCATE TABLE`，資料庫層級與伺服器層級 DDL 皆被封鎖。
- 混合 DML 與 DDL 的多語句批次會被拒絕。
- 透過單次解析去除注解與字串字面值，防止以注解隱藏 DML 繞過關鍵字檢查。
- 每次嘗試均以單行 JSON 稽核記錄寫入 stderr（verdict、op、rows、duration、已遮蔽 SQL）。

### `execute_write` 參數

| 參數 | 型別 | 說明 |
|------|------|------|
| `sql` | 字串（必填） | 要執行的 SQL 語句 |
| `confirm` | 布林值 | 設為 `true` 以授權 `TRUNCATE`/`DROP`；除非設定 `dry_run`，否則這些動詞皆需此參數 |
| `dry_run` | 布林值 | 在永遠回滾的交易中執行並回報預計影響列數；同時取消 `confirm` 的要求 |

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
