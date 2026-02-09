package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/microsoft/go-mssqldb"
)

var db *sql.DB

func initDB() error {
	connStr := os.Getenv("MSSQL_CONNECTION_STRING")
	if connStr == "" {
		return fmt.Errorf("MSSQL_CONNECTION_STRING environment variable is required")
	}

	var err error
	db, err = sql.Open("sqlserver", connStr)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	return nil
}
