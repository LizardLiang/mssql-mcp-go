package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// writeDB is the minimal database interface required by the write handler.
// Named interface enables test injection via sqlmock without depending on *sql.DB (S5).
type writeDB interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// auditVerdict is the typed enum for the handler verdict (S6).
type auditVerdict string

const (
	verdictAccepted auditVerdict = "accepted"
	verdictRejected auditVerdict = "rejected"
	verdictDryRun   auditVerdict = "dry-run"
)

// auditCtx is the handler-local mutable struct that carries audit fields from
// various points in the handler down to the defer emitAudit(&auditCtx) call.
// Each handler invocation has its own; no shared state across calls.
type auditCtx struct {
	startedAt time.Time    // set at handler entry
	op        string       // verb token; "" until classification completes
	verdict   auditVerdict // "" (default) | "accepted" | "rejected" | "dry-run"
	rows      int64        // 0 default; set after SELECT @@ROWCOUNT or to would-be count on cap exceeded
	reason    string       // rejection reason; "" on accepted
	rawSQL    string       // populated at handler entry; never mutated thereafter
}

// emitAudit is the deferred function that writes the audit line.
// It uses recover() to ensure the audit line is emitted even on panic.
// After emitting, it re-panics to preserve the original panic behavior.
// Parameter w is io.Writer — the dead interface assertion has been removed (W9).
func emitAudit(ctx *auditCtx, cfg WriteConfig, w io.Writer) {
	// Recover from panic — guarantees the audit line is emitted even if the handler panics.
	r := recover()
	if r != nil {
		if ctx.verdict == "" {
			ctx.verdict = verdictRejected
			ctx.reason = fmt.Sprintf("panic: %v", r)
		}
	}

	// Defensive: if no explicit verdict was ever set, default to rejected/internal-error.
	if ctx.verdict == "" {
		ctx.verdict = verdictRejected
		ctx.reason = string(reasonInternal)
	}

	entry := auditEntry{
		TS:      time.Now().UTC().Format(time.RFC3339),
		Op:      ctx.op,
		Verdict: string(ctx.verdict),
		Rows:    ctx.rows,
		DurMS:   time.Since(ctx.startedAt).Milliseconds(),
		Reason:  ctx.reason,
		SQL:     prepareAuditSQL(ctx.rawSQL, cfg),
	}

	if err := writeAuditLine(w, entry); err != nil {
		atomic.AddUint64(&auditFailureCount, 1)
	}

	// Re-panic after ensuring audit emission
	if r != nil {
		panic(r)
	}
}

// mapDBError maps a database error to the appropriate audit rejection reason and message.
// It distinguishes write-timeout (context deadline) from client-cancellation and generic DB errors.
// This replaces the 4 duplicated inline error checks (W8, M-004).
func mapDBError(ctx context.Context, err error, timeout time.Duration) (reason rejectionReason, msg string) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return reasonWriteTimeout, fmt.Sprintf("execute_write: write timeout: statement exceeded %s", timeout)
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return reasonClientCancelled, fmt.Sprintf("execute_write: client cancelled: %v", err)
	}
	return reasonDBError, fmt.Sprintf("execute_write: database error: %v", err)
}

// buildExecuteWriteDescription returns the FR-022 compliant tool description
// with current cap and timeout values substituted.
func buildExecuteWriteDescription(cfg WriteConfig) string {
	return fmt.Sprintf(`Executes a write SQL statement. Rules:
- WHERE required for UPDATE and DELETE.
- MERGE requires ON.
- TRUNCATE/DROP require confirm: true (unless dry_run is true; dry_run overrides confirm).
- Multi-statement payloads are rejected unless every statement is a whitelisted DDL (CREATE/ALTER/DROP TABLE/INDEX, TRUNCATE TABLE).
- Object scope: only table/index DDL; DATABASE/LOGIN/SCHEMA/PROCEDURE/VIEW/TRIGGER/GRANT etc. are rejected.
- max rows: %d  (DML/MERGE; DDL/TRUNCATE bypass the cap)
- write timeout: %s
- dry_run runs inside a transaction that always rolls back; reports would-be rows.
- WHERE/ON detection ignores SQL comments and quoted literals.`, cfg.MaxRows, cfg.WriteTimeout.String())
}

// registerWriteTool adds the execute_write tool to the MCP server.
func registerWriteTool(s *server.MCPServer, cfg WriteConfig) {
	handler := makeExecuteWriteHandler(cfg)
	s.AddTool(mcp.NewTool("execute_write",
		mcp.WithDescription(buildExecuteWriteDescription(cfg)),
		mcp.WithString("sql", mcp.Required(),
			mcp.Description("SQL statement to execute. Single statement for DML; pure-DDL batches allowed (CREATE/ALTER/DROP TABLE/INDEX, TRUNCATE TABLE).")),
		mcp.WithBoolean("confirm",
			mcp.Description("Set true to authorize TRUNCATE/DROP. Required for those verbs unless dry_run is true.")),
		mcp.WithBoolean("dry_run",
			mcp.Description("When true, runs inside a transaction that always rolls back; reports would-be rows. Overrides confirm requirement.")),
	), handler)
}

// makeExecuteWriteHandler creates a closure handler for execute_write with the given config.
// This allows the config to be captured and for test injection of the audit writer.
func makeExecuteWriteHandler(cfg WriteConfig) func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleExecuteWriteWithWriterAndDB(ctx, request, cfg, os.Stderr, db)
	}
}

// handleExecuteWriteWithWriterAndDB is the fully injectable version for testing.
// Both the audit writer (io.Writer) and the database (writeDB) can be injected.
// The function delegates to a writeHandler struct for phase-separated execution (W10).
func handleExecuteWriteWithWriterAndDB(ctx context.Context, request mcp.CallToolRequest, cfg WriteConfig, auditWriter io.Writer, sqlDB writeDB) (*mcp.CallToolResult, error) {
	h := &writeHandler{
		cfg:    cfg,
		ctx:    ctx,
		db:     sqlDB,
		writer: auditWriter,
		ac: &auditCtx{
			startedAt: time.Now(),
		},
	}
	return h.run(request)
}

// writeHandler owns all per-request state and implements the handler phases (W10).
type writeHandler struct {
	ac     *auditCtx
	cfg    WriteConfig
	ctx    context.Context
	db     writeDB
	writer io.Writer
}

// run orchestrates all handler phases and ensures audit emission on every exit path.
func (h *writeHandler) run(request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Defer audit emission — runs even on panic.
	defer emitAudit(h.ac, h.cfg, h.writer)

	// Phase 1: Extract and validate arguments.
	rawSQL, confirm, dryRun, earlyResult := h.extractArgs(request)
	if earlyResult != nil {
		return earlyResult, nil
	}

	// Phase 2: Lex and classify.
	batch, earlyResult := h.lexAndClassify(rawSQL)
	if earlyResult != nil {
		return earlyResult, nil
	}

	// Phase 3: Enforce policy.
	if earlyResult = h.enforcePolicy(batch, confirm, dryRun); earlyResult != nil {
		return earlyResult, nil
	}

	// Phase 4: Execute within a transaction.
	return h.executeAndFinalize(batch, rawSQL, dryRun)
}

// extractArgs pulls sql/confirm/dry_run from the MCP request and validates sql is non-empty.
func (h *writeHandler) extractArgs(request mcp.CallToolRequest) (rawSQL string, confirm bool, dryRun bool, earlyResult *mcp.CallToolResult) {
	args := getArgs(request)
	rawSQL, _ = args["sql"].(string)
	confirm, _ = args["confirm"].(bool)
	dryRun, _ = args["dry_run"].(bool)

	h.ac.rawSQL = rawSQL

	if rawSQL == "" {
		h.ac.verdict = verdictRejected
		h.ac.reason = string(reasonMalformedSQL)
		return "", false, false, mcp.NewToolResultError("execute_write: sql parameter is required")
	}
	return rawSQL, confirm, dryRun, nil
}

// lexAndClassify runs the SQL lexer and classifier, populating h.ac.op on success.
func (h *writeHandler) lexAndClassify(rawSQL string) (batch ClassifiedBatch, earlyResult *mcp.CallToolResult) {
	working, err := lexSQL(rawSQL)
	if err != nil {
		h.ac.verdict = verdictRejected
		h.ac.reason = string(reasonMalformedSQL)
		return ClassifiedBatch{}, mcp.NewToolResultError("execute_write: " + err.Error())
	}

	batch, err = classifyBatch(working, rawSQL)
	if err != nil {
		h.ac.verdict = verdictRejected
		h.ac.reason = string(reasonMalformedSQL)
		return ClassifiedBatch{}, mcp.NewToolResultError("execute_write: " + err.Error())
	}

	if len(batch.Statements) > 0 {
		h.ac.op = opStringFromBatch(batch)
	}
	return batch, nil
}

// enforcePolicy applies the policy rules to the classified batch.
func (h *writeHandler) enforcePolicy(batch ClassifiedBatch, confirm bool, dryRun bool) *mcp.CallToolResult {
	if polErr := enforcePolicy(batch, h.cfg, confirm, dryRun); polErr != nil {
		h.ac.verdict = verdictRejected
		h.ac.reason = string(polErr.Reason)
		return mcp.NewToolResultError(polErr.Message)
	}
	return nil
}

// executeAndFinalize begins a transaction, executes the SQL, checks the row cap,
// and either commits (normal) or rolls back (dry-run, cap exceeded, error).
func (h *writeHandler) executeAndFinalize(batch ClassifiedBatch, rawSQL string, dryRun bool) (*mcp.CallToolResult, error) {
	ctx2, cancel := context.WithTimeout(h.ctx, h.cfg.WriteTimeout)
	defer cancel()

	tx, err := h.db.BeginTx(ctx2, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		h.ac.verdict = verdictRejected
		reason, msg := mapDBError(ctx2, err, h.cfg.WriteTimeout)
		h.ac.reason = string(reason)
		return mcp.NewToolResultError(msg), nil
	}
	// Safety net: rollback on any unhandled exit path (no-op after Commit).
	// Nil guard prevents panic if tx somehow wasn't assigned (M-005). //nolint:errcheck
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	// Execute the user SQL with SET NOCOUNT ON prepended.
	_, err = tx.ExecContext(ctx2, "SET NOCOUNT ON; "+rawSQL)
	if err != nil {
		h.ac.verdict = verdictRejected
		reason, msg := mapDBError(ctx2, err, h.cfg.WriteTimeout)
		h.ac.reason = string(reason)
		return mcp.NewToolResultError(msg), nil
	}

	// Defensive read of the user-visible row count (Apollo Concern 1 resolution).
	// @@ROWCOUNT is updated by MSSQL even when SET NOCOUNT ON is active.
	var affected int64
	err = tx.QueryRowContext(ctx2, "SELECT @@ROWCOUNT AS rows").Scan(&affected)
	if err != nil {
		h.ac.verdict = verdictRejected
		reason, msg := mapDBError(ctx2, err, h.cfg.WriteTimeout)
		h.ac.reason = string(reason)
		return mcp.NewToolResultError(msg), nil
	}

	h.ac.rows = affected // tentative; may be overwritten if cap check fires

	// Cap check: applies to DML/MERGE but NOT pure TRUNCATE/DDL.
	// When HasDML==true and it's not all-TRUNCATE, isDDLOnly is necessarily false — drop it (W7).
	if batch.HasDML && !batchIsAllTruncate(batch) {
		if affected > int64(h.cfg.MaxRows) {
			// Rollback happens via deferred tx.Rollback() above.
			h.ac.verdict = verdictRejected
			h.ac.reason = string(reasonRowCapExceeded)
			return mcp.NewToolResultError(fmt.Sprintf(
				"execute_write: row limit exceeded: would affect %d rows (limit: %d); rolled back. Tighten the WHERE clause or ask the operator to raise --max-write-rows.",
				affected, h.cfg.MaxRows)), nil
		}
	}

	// Dry run: rollback (via defer) and report simulated count.
	if dryRun {
		h.ac.verdict = verdictDryRun

		type dryRunResult struct {
			Verdict               string `json:"verdict"`
			Op                    string `json:"op"`
			RowsAffectedSimulated int64  `json:"rows_affected_simulated"`
			DryRun                bool   `json:"dry_run"`
			DurMS                 int64  `json:"dur_ms"`
		}
		res := dryRunResult{
			Verdict:               string(verdictDryRun),
			Op:                    h.ac.op,
			RowsAffectedSimulated: affected,
			DryRun:                true,
			DurMS:                 time.Since(h.ac.startedAt).Milliseconds(),
		}
		out, _ := json.MarshalIndent(res, "", " ") // 1-space indent matches tools.go (W11)
		return mcp.NewToolResultText(string(out)), nil
	}

	// Commit.
	if err := tx.Commit(); err != nil {
		h.ac.verdict = verdictRejected
		reason, msg := mapDBError(ctx2, err, h.cfg.WriteTimeout)
		h.ac.reason = string(reason)
		return mcp.NewToolResultError(msg), nil
	}

	// Success.
	h.ac.verdict = verdictAccepted

	type successResult struct {
		Verdict      string `json:"verdict"`
		Op           string `json:"op"`
		RowsAffected int64  `json:"rows_affected"`
		DurMS        int64  `json:"dur_ms"`
	}
	res := successResult{
		Verdict:      string(verdictAccepted),
		Op:           h.ac.op,
		RowsAffected: affected,
		DurMS:        time.Since(h.ac.startedAt).Milliseconds(),
	}
	out, _ := json.MarshalIndent(res, "", " ") // 1-space indent matches tools.go (W11)
	return mcp.NewToolResultText(string(out)), nil
}
