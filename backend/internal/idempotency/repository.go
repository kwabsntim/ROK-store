package idempotency

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Repository handles idempotency key persistence.
type Repository struct {
	db *sql.DB
}

// NewRepository creates a new idempotency Repository.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// KeyRecord holds the current state of a stored idempotency key.
type KeyRecord struct {
	Key          string
	UserID       sql.NullString
	GuestEmail   sql.NullString
	RequestPath  string
	ResponseCode sql.NullInt32
	ResponseBody sql.NullString
	Status       string
	ExpiresAt    time.Time
}

// Get retrieves an idempotency key record matching the key and either the
// user_id or guest_email. Returns sql.ErrNoRows if not found or expired.
func (r *Repository) Get(ctx context.Context, key string, userID sql.NullString, guestEmail sql.NullString) (*KeyRecord, error) {
	var rec KeyRecord
	err := r.db.QueryRowContext(ctx,
		`SELECT key, user_id, guest_email, request_path, response_code, response_body, status, expires_at
		 FROM idempotency_keys
		 WHERE key = ?
		   AND (user_id = ? OR guest_email = ?)
		   AND expires_at > CURRENT_TIMESTAMP`,
		key, userID, guestEmail,
	).Scan(
		&rec.Key, &rec.UserID, &rec.GuestEmail, &rec.RequestPath,
		&rec.ResponseCode, &rec.ResponseBody, &rec.Status, &rec.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// Lock inserts a new idempotency key with status 'started'.
// Returns an error if the key already exists (unique constraint).
func (r *Repository) Lock(ctx context.Context, key, path string, userID sql.NullString, guestEmail sql.NullString, ttl time.Duration) error {
	expiresAt := time.Now().Add(ttl)
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO idempotency_keys (key, user_id, guest_email, request_path, status, expires_at)
		 VALUES (?, ?, ?, ?, 'started', ?)`,
		key, userID, guestEmail, path, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("lock idempotency key: %w", err)
	}
	return nil
}

// Complete marks an idempotency key as 'completed' and caches the response.
func (r *Repository) Complete(ctx context.Context, key string, responseCode int, responseBody string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE idempotency_keys
		 SET status = 'completed', response_code = ?, response_body = ?
		 WHERE key = ?`,
		responseCode, responseBody, key,
	)
	return err
}

// Fail marks an idempotency key as 'failed'.
func (r *Repository) Fail(ctx context.Context, key string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE idempotency_keys SET status = 'failed' WHERE key = ?`, key)
	return err
}

// Cleanup deletes all expired idempotency keys.
func (r *Repository) Cleanup(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE expires_at <= CURRENT_TIMESTAMP`)
	return err
}
