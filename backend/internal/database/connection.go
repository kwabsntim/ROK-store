package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/tursodatabase/go-libsql"
)

// DB is the application-wide database handle.
var DB *sql.DB

// Connect opens a connection to the SQLite/Turso database defined in
// the DATABASE_URL environment variable and configures the connection pool.
func Connect() (*sql.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "file:rokstore.db"
		log.Println("[database] DATABASE_URL not set, defaulting to file:rokstore.db")
	}

	db, err := sql.Open("libsql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// For local file-based SQLite we must restrict writes to a single connection
	// to avoid SQLITE_BUSY errors.  Turso handles concurrency at the server level,
	// but keeping MaxOpenConns=1 is still safe there.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err = db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	log.Println("[database] connected successfully")
	DB = db
	return db, nil
}
