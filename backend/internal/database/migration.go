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

	// ── Additive column migrations (safe to run repeatedly) ──
	// ALTER TABLE ADD COLUMN is a no-op if the column already exists in Turso/SQLite.
	additives := []string{
		`ALTER TABLE orders ADD COLUMN fulfilled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE orders ADD COLUMN fulfilled_at DATETIME`,
	}
	for _, stmt := range additives {
		if _, err = db.Exec(stmt); err != nil {
			// SQLite/Turso returns an error if the column already exists — that's fine, ignore it.
			if !strings.Contains(err.Error(), "duplicate column") && !strings.Contains(err.Error(), "already exists") {
				log.Printf("[database] additive migration warning: %v", err)
			}
		}
	}

	return nil
}
