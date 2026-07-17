package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Migrate reads schema.sql and executes each statement individually.
// Running statements one-by-one is required for Turso (go-libsql remote),
// which does not support multi-statement batch execution via db.Exec.
// All CREATE TABLE IF NOT EXISTS statements are idempotent — safe on every startup.
func Migrate(db *sql.DB) error {
	// Resolve schema.sql relative to this source file (../../schema.sql).
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("could not determine source file path")
	}
	schemaPath := filepath.Join(filepath.Dir(filename), "..", "..", "schema.sql")

	// Fallback: look for schema.sql next to the running binary.
	if _, err := os.Stat(schemaPath); os.IsNotExist(err) {
		schemaPath = "schema.sql"
	}

	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("failed to read schema.sql: %w", err)
	}

	// Split on semicolons and execute each statement individually.
	stmts := strings.Split(string(schema), ";")
	for _, raw := range stmts {
		stmt := strings.TrimSpace(raw)
		if stmt == "" {
			continue
		}
		if _, err = db.Exec(stmt); err != nil {
			return fmt.Errorf("failed to execute statement [%.60s...]: %w", stmt, err)
		}
	}

	log.Println("[database] migrations applied successfully")
	return nil
}
