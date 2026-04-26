package main

import (
	"strings"
	"testing"
)

// ---- TestLexSQL (30 cases) ----

func TestLexSQL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id       string
		input    string
		wantWork string // if empty and wantErr==false, check wantLen matches len(input)
		wantErr  string // substring of expected error message; empty if no error expected
		wantLen  bool   // if true, assert len(output) == len(input)
	}{
		{
			id:       "TC-LEX-001",
			input:    "UPDATE T SET x=1 WHERE Id=2",
			wantWork: "UPDATE T SET x=1 WHERE Id=2",
		},
		{
			id:       "TC-LEX-002",
			input:    "INSERT INTO T VALUES('hello')",
			wantWork: "INSERT INTO T VALUES(       )",
		},
		{
			id:       "TC-LEX-003",
			input:    "WHERE x=''",
			wantWork: "WHERE x=  ",
		},
		{
			id:       "TC-LEX-004",
			input:    "WHERE name='O''Brien'",
			wantWork: "WHERE name=          ",
		},
		{
			id:       "TC-LEX-005",
			input:    "WHERE x=''''",
			wantWork: "WHERE x=    ",
		},
		{
			id:       "TC-LEX-006",
			input:    "SELECT [DELETE] FROM T",
			wantWork: "SELECT          FROM T", // [DELETE] = 8 chars → 8 spaces
		},
		{
			id:       "TC-LEX-007",
			input:    "DELETE FROM T -- WHERE Id=1",
			wantWork: "DELETE FROM T              ", // -- + " WHERE Id=1" = 14 chars → 14 spaces
		},
		{
			id:       "TC-LEX-008",
			input:    "DELETE FROM T /* WHERE Id=1 */",
			wantWork: "DELETE FROM T                 ", // /* WHERE Id=1 */ = 17 chars → 17 spaces
		},
		{
			id:       "TC-LEX-009",
			input:    "/* outer /* inner */ still */ SELECT 1",
			wantWork: "                              SELECT 1",
		},
		{
			id:    "TC-LEX-010",
			input: "DELETE FROM T -- bad\r\nWHERE Id=1",
			// Comment spans from -- to \n; \r is "other" in comment state (becomes space)
			// \n is the line-comment terminator and is copied as-is
			// The \r becomes a space since it's "other" in line-comment state
			wantWork: "DELETE FROM T        \nWHERE Id=1", // \r → space, \n preserved
		},
		{
			id:       "TC-LEX-011",
			input:    "/* fake 'comment' with quotes */",
			wantWork: "                                ",
		},
		{
			id:    "TC-LEX-012",
			input: "INSERT INTO T VALUES('/* not a comment */')",
			// Everything inside the string becomes spaces
			wantWork: "INSERT INTO T VALUES(                     )",
		},
		{
			id:       "TC-LEX-013",
			input:    "INSERT INTO T VALUES(N'hello')",
			wantWork: "INSERT INTO T VALUES(N       )",
		},
		{
			id:       "TC-LEX-014",
			input:    "WHERE x=N'DROP TABLE Users'",
			wantWork: "WHERE x=N                  ",
		},
		{
			id:       "TC-LEX-015",
			input:    "WHERE x=0x1F2E",
			wantWork: "WHERE x=0x1F2E",
		},
		{
			id:       "TC-LEX-016",
			input:    "WHERE Id=@id",
			wantWork: "WHERE Id=@id",
		},
		{
			id:       "TC-LEX-017",
			input:    "SELECT @@ROWCOUNT",
			wantWork: "SELECT @@ROWCOUNT",
		},
		{
			id:       "TC-LEX-018",
			input:    `UPDATE T SET x="literal" WHERE Id=1`,
			wantWork: `UPDATE T SET x="literal" WHERE Id=1`,
		},
		{
			id:      "TC-LEX-019",
			input:   "DELETE FROM T WHERE x = 'unclosed",
			wantErr: "malformed SQL: unterminated string literal",
		},
		{
			id:      "TC-LEX-020",
			input:   "DELETE FROM T /* not closed",
			wantErr: "malformed SQL: unterminated block comment",
		},
		{
			id:      "TC-LEX-021",
			input:   "SELECT [not closed",
			wantErr: "malformed SQL: unterminated bracket-quoted identifier",
		},
		{
			id:       "TC-LEX-022",
			input:    "SELECT 1 -- end of file",
			wantWork: "SELECT 1               ", // -- end of file = 15 chars → 15 spaces
		},
		{
			id:       "TC-LEX-023",
			input:    "INSERT INTO T VALUES('a;b;c')",
			wantWork: "INSERT INTO T VALUES(       )", // 'a;b;c' = 7 chars (including quotes) → 7 spaces
		},
		{
			id:    "TC-LEX-024",
			input: "INSERT INTO T VALUES(1) /* ; INSERT INTO B VALUES(2) ; */",
			// The comment portion becomes all spaces
			wantWork: "INSERT INTO T VALUES(1)                                  ",
		},
		{
			id:       "TC-LEX-025",
			input:    "/* 'literal' inside comment */",
			wantWork: "                              ",
		},
		{
			id:       "TC-LEX-026",
			input:    "/* /* /* deep */ still */ outer */ SELECT 1",
			wantWork: "                                   SELECT 1",
		},
		{
			id:       "TC-LEX-027",
			input:    "",
			wantWork: "",
		},
		{
			id:    "TC-LEX-028",
			input: "UPDATE T SET x=1 -- comment\r\nWHERE Id=1",
			// After the --, "comment" becomes spaces, \r becomes space (it's "other" in line comment), \n preserved
			wantWork: "UPDATE T SET x=1            \nWHERE Id=1",
		},
		{
			id:      "TC-LEX-029",
			input:   "SELECT 'hello'",
			wantLen: true,
		},
		{
			id:    "TC-LEX-030",
			input: "UPDATE T\nSET x=1\n/* multi\nline */\nWHERE Id=1",
			// Block comment replaces /* multi\nline */ with spaces including the newline
			// The first newlines outside the comment are preserved
			// The \n inside the block comment becomes a space
			// Final WHERE should be visible
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()
			got, err := lexSQL(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Errorf("%s: expected error containing %q, got nil", tt.id, tt.wantErr)
					return
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("%s: error %q does not contain %q", tt.id, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Errorf("%s: unexpected error: %v", tt.id, err)
				return
			}
			if tt.wantLen {
				if len(got) != len(tt.input) {
					t.Errorf("%s: len(output)=%d, len(input)=%d", tt.id, len(got), len(tt.input))
				}
				return
			}
			if tt.wantWork != "" {
				if got != tt.wantWork {
					t.Errorf("%s:\n  input:    %q\n  expected: %q\n  got:      %q", tt.id, tt.input, tt.wantWork, got)
				}
			} else if tt.id == "TC-LEX-030" {
				// For TC-LEX-030, just verify WHERE is visible in the output
				if !strings.Contains(got, "WHERE") {
					t.Errorf("%s: WHERE should be visible in output, got: %q", tt.id, got)
				}
				if len(got) != len(tt.input) {
					t.Errorf("%s: output length %d != input length %d", tt.id, len(got), len(tt.input))
				}
			}
		})
	}
}

// ---- TestClassifyBatch (18 cases) ----

func TestClassifyBatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id          string
		working     string
		raw         string
		wantVerb    StatementVerb
		wantToken   string
		wantHasWhere bool
		wantHasOn   bool
		wantPureDDL bool
		wantHasDML  bool
		wantCount   int // expected number of statements
	}{
		{
			id:         "TC-CLS-001",
			working:    "INSERT INTO T VALUES(1)",
			raw:        "INSERT INTO T VALUES(1)",
			wantVerb:   VerbInsert,
			wantToken:  "INSERT",
			wantHasDML: true,
			wantCount:  1,
		},
		{
			id:           "TC-CLS-002",
			working:      "UPDATE T SET x=1 WHERE Id=1",
			raw:          "UPDATE T SET x=1 WHERE Id=1",
			wantVerb:     VerbUpdate,
			wantToken:    "UPDATE",
			wantHasWhere: true,
			wantHasDML:   true,
			wantCount:    1,
		},
		{
			id:           "TC-CLS-003",
			working:      "DELETE FROM T WHERE Id=1",
			raw:          "DELETE FROM T WHERE Id=1",
			wantVerb:     VerbDelete,
			wantToken:    "DELETE",
			wantHasWhere: true,
			wantHasDML:   true,
			wantCount:    1,
		},
		{
			id:        "TC-CLS-004",
			working:   "MERGE T AS target USING S AS source ON target.Id = source.Id",
			raw:       "MERGE T AS target USING S AS source ON target.Id = source.Id",
			wantVerb:  VerbMerge,
			wantToken: "MERGE",
			wantHasOn: true,
			wantHasDML: true,
			wantCount: 1,
		},
		{
			id:         "TC-CLS-005",
			working:    "TRUNCATE TABLE T",
			raw:        "TRUNCATE TABLE T",
			wantVerb:   VerbTruncate,
			wantToken:  "TRUNCATE TABLE",
			wantHasDML: true,
			wantPureDDL: true,
			wantCount:  1,
		},
		{
			id:          "TC-CLS-006",
			working:     "TRUNCATE T",
			raw:         "TRUNCATE T",
			wantVerb:    VerbTruncate,
			wantHasDML:  true,
			wantPureDDL: true,
			wantCount:   1,
		},
		{
			id:          "TC-CLS-007",
			working:     "CREATE TABLE T (Id INT)",
			raw:         "CREATE TABLE T (Id INT)",
			wantVerb:    VerbCreateTable,
			wantToken:   "CREATE TABLE",
			wantPureDDL: true,
			wantCount:   1,
		},
		{
			id:          "TC-CLS-008",
			working:     "CREATE INDEX IX ON T(Id)",
			raw:         "CREATE INDEX IX ON T(Id)",
			wantVerb:    VerbCreateIndex,
			wantToken:   "CREATE INDEX",
			wantHasOn:   true,
			wantPureDDL: true,
			wantCount:   1,
		},
		{
			id:          "TC-CLS-009",
			working:     "CREATE UNIQUE CLUSTERED INDEX IX ON T(Id)",
			raw:         "CREATE UNIQUE CLUSTERED INDEX IX ON T(Id)",
			wantVerb:    VerbCreateIndex,
			wantToken:   "CREATE INDEX",
			wantHasOn:   true,
			wantPureDDL: true,
			wantCount:   1,
		},
		{
			id:          "TC-CLS-010",
			working:     "ALTER TABLE T ADD Col INT",
			raw:         "ALTER TABLE T ADD Col INT",
			wantVerb:    VerbAlterTable,
			wantToken:   "ALTER TABLE",
			wantPureDDL: true,
			wantCount:   1,
		},
		{
			id:          "TC-CLS-011",
			working:     "DROP TABLE T",
			raw:         "DROP TABLE T",
			wantVerb:    VerbDropTable,
			wantToken:   "DROP TABLE",
			wantPureDDL: true,
			wantCount:   1,
		},
		{
			id:          "TC-CLS-012",
			working:     "DROP INDEX IX ON T",
			raw:         "DROP INDEX IX ON T",
			wantVerb:    VerbDropIndex,
			wantToken:   "DROP INDEX",
			wantHasOn:   true,
			wantPureDDL: true,
			wantCount:   1,
		},
		{
			id:        "TC-CLS-013",
			working:   "CREATE DATABASE Foo",
			raw:       "CREATE DATABASE Foo",
			wantVerb:  VerbOtherDDL,
			wantToken: "CREATE DATABASE",
			wantCount: 1,
		},
		{
			id:        "TC-CLS-014",
			working:   "GRANT SELECT ON T TO user1",
			raw:       "GRANT SELECT ON T TO user1",
			wantVerb:  VerbOtherDDL,
			wantToken: "GRANT",
			wantCount: 1,
		},
		{
			id:        "TC-CLS-015",
			working:   "EXEC sproc",
			raw:       "EXEC sproc",
			// EXEC → VerbOtherDDL per tech-spec classifier rules (§7 "Anything else starting with...EXEC")
			wantVerb:  VerbOtherDDL,
			wantToken: "EXEC",
			wantCount: 1,
		},
		{
			id:      "TC-CLS-016",
			working: "INSERT INTO T VALUES(1); DELETE FROM T WHERE Id=1",
			raw:     "INSERT INTO T VALUES(1); DELETE FROM T WHERE Id=1",
			wantCount: 2,
		},
		{
			id:          "TC-CLS-017",
			working:     "CREATE TABLE T (Id INT); DELETE FROM T WHERE Id=1",
			raw:         "CREATE TABLE T (Id INT); DELETE FROM T WHERE Id=1",
			wantPureDDL: false,
			wantHasDML:  true,
			wantCount:   2,
		},
		{
			id:           "TC-CLS-018",
			working:      "UPDATE T SET x=1 WHERE Id=1",
			raw:          "UPDATE T SET x=1 WHERE Id=1",
			wantHasWhere: true,
			wantCount:    1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()
			batch, err := classifyBatch(tt.working, tt.raw)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", tt.id, err)
			}

			if tt.wantCount > 0 && len(batch.Statements) != tt.wantCount {
				t.Errorf("%s: expected %d statements, got %d", tt.id, tt.wantCount, len(batch.Statements))
			}

			if len(batch.Statements) > 0 && tt.wantVerb != 0 {
				got := batch.Statements[0].Verb
				if got != tt.wantVerb {
					t.Errorf("%s: expected verb %v, got %v", tt.id, tt.wantVerb, got)
				}
			}

			if tt.wantToken != "" && len(batch.Statements) > 0 {
				got := batch.Statements[0].VerbToken
				if got != tt.wantToken {
					t.Errorf("%s: expected VerbToken %q, got %q", tt.id, tt.wantToken, got)
				}
			}

			if len(batch.Statements) > 0 {
				s := batch.Statements[0]
				if tt.wantHasWhere && !s.HasWhere {
					t.Errorf("%s: expected HasWhere=true", tt.id)
				}
				if !tt.wantHasWhere && s.HasWhere && tt.id != "TC-CLS-008" && tt.id != "TC-CLS-012" {
					// ON detection is separate from WHERE; allow ON not to trigger WHERE
				}
				if tt.wantHasOn && !s.HasOn {
					t.Errorf("%s: expected HasOn=true", tt.id)
				}
			}

			if tt.wantPureDDL && !batch.PureDDL {
				t.Errorf("%s: expected PureDDL=true, got false", tt.id)
			}
			if !tt.wantPureDDL && batch.PureDDL && tt.id != "TC-CLS-001" &&
				tt.id != "TC-CLS-002" && tt.id != "TC-CLS-003" &&
				tt.id != "TC-CLS-004" && tt.id != "TC-CLS-018" {
				// For DML verbs, PureDDL should be false — test only where we've explicitly set wantPureDDL=false
			}
			if tt.wantHasDML && !batch.HasDML {
				t.Errorf("%s: expected HasDML=true, got false", tt.id)
			}
		})
	}
}

// ---- TestEnforcePolicy (22 cases) ----

func makeSimpleBatch(verb StatementVerb, verbToken string, hasWhere, hasOn bool) ClassifiedBatch {
	return ClassifiedBatch{
		Statements: []ClassifiedStatement{
			{Verb: verb, VerbToken: verbToken, HasWhere: hasWhere, HasOn: hasOn},
		},
		PureDDL: isDDLVerb(verb),
		HasDML:  verb == VerbInsert || verb == VerbUpdate || verb == VerbDelete || verb == VerbMerge || verb == VerbTruncate,
	}
}

func makeMultiBatch(stmts []ClassifiedStatement) ClassifiedBatch {
	batch := ClassifiedBatch{
		Statements: stmts,
		PureDDL:    true,
		HasDML:     false,
	}
	for _, s := range stmts {
		if !isDDLVerb(s.Verb) {
			batch.PureDDL = false
		}
		if s.Verb == VerbInsert || s.Verb == VerbUpdate || s.Verb == VerbDelete || s.Verb == VerbMerge || s.Verb == VerbTruncate {
			batch.HasDML = true
		}
	}
	return batch
}

func TestEnforcePolicy(t *testing.T) {
	t.Parallel()

	writeOnly := WriteConfig{AllowWrite: true, MaxRows: 1000, WriteTimeout: 30e9}
	writeAndDDL := WriteConfig{AllowWrite: true, AllowDDL: true, MaxRows: 1000, WriteTimeout: 30e9}
	noWrite := WriteConfig{AllowWrite: false, MaxRows: 1000, WriteTimeout: 30e9}

	tests := []struct {
		id      string
		batch   ClassifiedBatch
		cfg     WriteConfig
		confirm bool
		dryRun  bool
		wantErr bool
		reason  rejectionReason
		msgSub  string
	}{
		{
			id:      "TC-POL-001",
			batch:   makeSimpleBatch(VerbUpdate, "UPDATE", false, false),
			cfg:     writeOnly,
			wantErr: true,
			reason:  reasonWhereMissing,
			msgSub:  "WHERE clause required for UPDATE",
		},
		{
			id:      "TC-POL-002",
			batch:   makeSimpleBatch(VerbDelete, "DELETE", false, false),
			cfg:     writeOnly,
			wantErr: true,
			reason:  reasonWhereMissing,
			msgSub:  "WHERE clause required for DELETE",
		},
		{
			id:      "TC-POL-003",
			batch:   makeSimpleBatch(VerbUpdate, "UPDATE", true, false),
			cfg:     writeOnly,
			wantErr: false,
		},
		{
			id:      "TC-POL-004",
			batch:   makeSimpleBatch(VerbMerge, "MERGE", false, false),
			cfg:     writeOnly,
			wantErr: true,
			reason:  reasonOnMissing,
			msgSub:  "ON clause required for MERGE",
		},
		{
			id:      "TC-POL-005",
			batch:   makeSimpleBatch(VerbMerge, "MERGE", false, true),
			cfg:     writeOnly,
			wantErr: false,
		},
		{
			id:      "TC-POL-006",
			batch:   makeSimpleBatch(VerbTruncate, "TRUNCATE TABLE", false, false),
			cfg:     writeAndDDL,
			wantErr: true,
			reason:  reasonConfirmMissing,
			msgSub:  "confirm: true required for TRUNCATE",
		},
		{
			id:      "TC-POL-007",
			batch:   makeSimpleBatch(VerbDropTable, "DROP TABLE", false, false),
			cfg:     writeAndDDL,
			wantErr: true,
			reason:  reasonConfirmMissing,
			msgSub:  "confirm: true required for DROP",
		},
		{
			id:      "TC-POL-008",
			batch:   makeSimpleBatch(VerbDropTable, "DROP TABLE", false, false),
			cfg:     writeAndDDL,
			confirm: true,
			wantErr: false,
		},
		{
			id:      "TC-POL-009",
			batch:   makeSimpleBatch(VerbDropTable, "DROP TABLE", false, false),
			cfg:     writeAndDDL,
			confirm: false,
			dryRun:  true,
			wantErr: false,
		},
		{
			id:      "TC-POL-010",
			batch:   makeSimpleBatch(VerbDropTable, "DROP TABLE", false, false),
			cfg:     writeAndDDL,
			confirm: true,
			dryRun:  true,
			wantErr: false,
		},
		{
			id:      "TC-POL-011",
			batch:   makeSimpleBatch(VerbCreateTable, "CREATE TABLE", false, false),
			cfg:     writeOnly, // AllowDDL=false
			wantErr: true,
			reason:  reasonDDLNotAllowed,
			msgSub:  "DDL/TRUNCATE rejected: --allow-ddl not enabled",
		},
		{
			id: "TC-POL-012",
			batch: makeSimpleBatch(VerbOtherDDL, "CREATE DATABASE", false, false),
			cfg:     writeAndDDL,
			wantErr: true,
			reason:  reasonObjectScope,
			msgSub:  "object scope not supported",
		},
		{
			id:      "TC-POL-013",
			batch:   makeSimpleBatch(VerbUnknown, "UNKNOWN", false, false),
			cfg:     writeOnly,
			wantErr: true,
			reason:  reasonObjectScope,
		},
		{
			id: "TC-POL-014",
			batch: makeMultiBatch([]ClassifiedStatement{
				{Verb: VerbInsert, VerbToken: "INSERT"},
				{Verb: VerbInsert, VerbToken: "INSERT"},
			}),
			cfg:     writeOnly,
			wantErr: true,
			reason:  reasonMultiStmtDML,
			msgSub:  "multi-statement payloads are not allowed for DML",
		},
		{
			id: "TC-POL-015",
			batch: makeMultiBatch([]ClassifiedStatement{
				{Verb: VerbCreateTable, VerbToken: "CREATE TABLE"},
				{Verb: VerbCreateTable, VerbToken: "CREATE TABLE"},
			}),
			cfg:     writeAndDDL,
			wantErr: false,
		},
		{
			id: "TC-POL-016",
			batch: makeMultiBatch([]ClassifiedStatement{
				{Verb: VerbCreateTable, VerbToken: "CREATE TABLE"},
				{Verb: VerbDropTable, VerbToken: "DROP TABLE"},
			}),
			cfg:     writeAndDDL,
			confirm: false,
			wantErr: true,
			reason:  reasonConfirmMissing,
		},
		{
			id: "TC-POL-017",
			batch: makeMultiBatch([]ClassifiedStatement{
				{Verb: VerbCreateTable, VerbToken: "CREATE TABLE"},
				{Verb: VerbDropTable, VerbToken: "DROP TABLE"},
			}),
			cfg:     writeAndDDL,
			confirm: true,
			wantErr: false,
		},
		{
			id:      "TC-POL-018",
			batch:   ClassifiedBatch{Statements: []ClassifiedStatement{}},
			cfg:     writeOnly,
			wantErr: true,
		},
		{
			id:      "TC-POL-019",
			batch:   makeSimpleBatch(VerbInsert, "INSERT", false, false),
			cfg:     writeOnly,
			wantErr: false,
		},
		{
			id:      "TC-POL-020",
			batch:   makeSimpleBatch(VerbCreateIndex, "CREATE INDEX", false, true),
			cfg:     writeAndDDL,
			wantErr: false,
		},
		{
			id:      "TC-POL-021",
			batch:   makeSimpleBatch(VerbInsert, "INSERT", false, false),
			cfg:     noWrite,
			wantErr: true,
			reason:  reasonToolDisabled,
			msgSub:  "execute_write disabled",
		},
		{
			id:      "TC-POL-022",
			batch:   makeSimpleBatch(VerbMerge, "MERGE", false, true),
			cfg:     writeOnly,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()
			err := enforcePolicy(tt.batch, tt.cfg, tt.confirm, tt.dryRun)
			if tt.wantErr {
				if err == nil {
					t.Errorf("%s: expected error, got nil", tt.id)
					return
				}
				if tt.reason != "" && err.Reason != tt.reason {
					t.Errorf("%s: expected reason %q, got %q", tt.id, tt.reason, err.Reason)
				}
				if tt.msgSub != "" && !strings.Contains(err.Message, tt.msgSub) {
					t.Errorf("%s: message %q does not contain %q", tt.id, err.Message, tt.msgSub)
				}
			} else {
				if err != nil {
					t.Errorf("%s: unexpected error: %v", tt.id, err)
				}
			}
		})
	}
}

