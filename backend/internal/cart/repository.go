package cart

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/kwabsntim/rok-store/internal/models"
)

// Repository handles cart-related database operations.
type Repository struct {
	db *sql.DB
}

// NewRepository creates a new cart Repository.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// GetByUserID returns all cart items for a user, with product details joined.
func (r *Repository) GetByUserID(ctx context.Context, userID string) ([]models.CartItem, float64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT ci.id, ci.user_id, ci.product_id, ci.quantity, COALESCE(ci.size,''), ci.created_at, ci.updated_at,
		        p.id, p.name, p.price, COALESCE(p.description,''), COALESCE(p.size,''), p.category, p.stock,
		        COALESCE(p.image_url,''), COALESCE(p.images,''), COALESCE(p.size_variants,''),
		        p.created_at, p.updated_at
		 FROM cart_items ci
		 JOIN products p ON p.id = ci.product_id
		 WHERE ci.user_id = ?
		 ORDER BY ci.created_at ASC`, userID,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("query cart: %w", err)
	}
	defer rows.Close()

	var items []models.CartItem
	var total float64

	for rows.Next() {
		var ci models.CartItem
		var p models.Product
		var sizeVariantsJSON string
		err := rows.Scan(
			&ci.ID, &ci.UserID, &ci.ProductID, &ci.Quantity, &ci.Size, &ci.CreatedAt, &ci.UpdatedAt,
			&p.ID, &p.Name, &p.Price, &p.Description, &p.Size, &p.Category, &p.Stock,
			&p.ImageURL, &p.Images, &sizeVariantsJSON,
			&p.CreatedAt, &p.UpdatedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("scan cart row: %w", err)
		}
		p.SizeVariants = parseSizeVariants(sizeVariantsJSON)
		ci.Product = &p
		total += p.Price * float64(ci.Quantity)
		items = append(items, ci)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// AddOrIncrement adds a product+size combo to the user's cart. If the exact
// product+size already exists, its quantity is incremented.
func (r *Repository) AddOrIncrement(ctx context.Context, userID, productID, size string, quantity int) (*models.CartItem, error) {
	now := time.Now()

	// Try to increment an existing row (matched by product_id AND size).
	res, err := r.db.ExecContext(ctx,
		`UPDATE cart_items SET quantity = quantity + ?, updated_at = ?
		 WHERE user_id = ? AND product_id = ? AND COALESCE(size,'') = ?`,
		quantity, now, userID, productID, size,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert cart item: %w", err)
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		// Not yet in cart — insert new row.
		id := uuid.NewString()
		_, err = r.db.ExecContext(ctx,
			`INSERT INTO cart_items (id, user_id, product_id, quantity, size, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, userID, productID, quantity, size, now, now,
		)
		if err != nil {
			return nil, fmt.Errorf("insert cart item: %w", err)
		}
	}

	return r.getItem(ctx, userID, productID, size)
}

// UpdateQuantity sets the exact quantity for a cart item identified by user+product+size.
// Returns sql.ErrNoRows if the item is not found.
func (r *Repository) UpdateQuantity(ctx context.Context, userID, productID, size string, quantity int) (*models.CartItem, error) {
	now := time.Now()
	res, err := r.db.ExecContext(ctx,
		`UPDATE cart_items SET quantity = ?, updated_at = ?
		 WHERE user_id = ? AND product_id = ? AND COALESCE(size,'') = ?`,
		quantity, now, userID, productID, size,
	)
	if err != nil {
		return nil, fmt.Errorf("update cart item: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, sql.ErrNoRows
	}
	return r.getItem(ctx, userID, productID, size)
}

// Remove deletes a cart item identified by user+product+size.
func (r *Repository) Remove(ctx context.Context, userID, productID, size string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM cart_items WHERE user_id = ? AND product_id = ? AND COALESCE(size,'') = ?`,
		userID, productID, size)
	if err != nil {
		return fmt.Errorf("delete cart item: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ClearByUserID removes all cart items for a user (called post-payment).
func (r *Repository) ClearByUserID(ctx context.Context, userID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM cart_items WHERE user_id = ?`, userID)
	return err
}

// ---- helpers ----

func (r *Repository) getItem(ctx context.Context, userID, productID, size string) (*models.CartItem, error) {
	var ci models.CartItem
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, product_id, quantity, COALESCE(size,''), created_at, updated_at
		 FROM cart_items WHERE user_id = ? AND product_id = ? AND COALESCE(size,'') = ?`,
		userID, productID, size,
	).Scan(&ci.ID, &ci.UserID, &ci.ProductID, &ci.Quantity, &ci.Size, &ci.CreatedAt, &ci.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &ci, nil
}

// parseSizeVariants deserializes size_variants JSON from the DB.
func parseSizeVariants(raw string) []models.SizeVariant {
	if raw == "" {
		return nil
	}
	var v []models.SizeVariant
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	return v
}
