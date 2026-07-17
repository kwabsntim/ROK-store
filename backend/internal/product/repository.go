package product

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/kwabsntim/rok-store/internal/models"
)

// Repository handles all product-related database operations.
type Repository struct {
	db *sql.DB
}

// NewRepository creates a new product Repository.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// cols is every column returned in product SELECT queries.
const cols = `id, name, price,
	COALESCE(description,''), COALESCE(size,''), category, stock,
	COALESCE(image_url,''), COALESCE(images,''),
	created_at, updated_at`

// ListAll returns a paginated, optionally category-filtered list of products
// along with the real COUNT(*) total for the given filter.
func (r *Repository) ListAll(ctx context.Context, category string, page, pageSize int) ([]models.Product, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	var total int
	var rows *sql.Rows
	var err error

	if category != "" {
		err = r.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM products WHERE category = ?`, category).Scan(&total)
	} else {
		err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM products`).Scan(&total)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("count products: %w", err)
	}

	if category != "" {
		rows, err = r.db.QueryContext(ctx,
			`SELECT `+cols+` FROM products WHERE category = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`,
			category, pageSize, offset)
	} else {
		rows, err = r.db.QueryContext(ctx,
			`SELECT `+cols+` FROM products ORDER BY created_at DESC LIMIT ? OFFSET ?`,
			pageSize, offset)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("query products: %w", err)
	}
	defer rows.Close()

	products, err := scanProducts(rows)
	return products, total, err
}

// ListNewest returns the 8 most recently added products for the "Fresh Arrivals" section.
func (r *Repository) ListNewest(ctx context.Context) ([]models.Product, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+cols+` FROM products ORDER BY created_at DESC LIMIT 8`)
	if err != nil {
		return nil, fmt.Errorf("query newest products: %w", err)
	}
	defer rows.Close()
	return scanProducts(rows)
}

// GetByID returns a single product by its UUID.
func (r *Repository) GetByID(ctx context.Context, id string) (*models.Product, error) {
	var p models.Product
	err := r.db.QueryRowContext(ctx,
		`SELECT `+cols+` FROM products WHERE id = ?`, id,
	).Scan(
		&p.ID, &p.Name, &p.Price, &p.Description, &p.Size, &p.Category, &p.Stock,
		&p.ImageURL, &p.Images,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err // callers check sql.ErrNoRows
	}
	return &p, nil
}

// Create inserts a new product and returns it with its generated ID and timestamps.
func (r *Repository) Create(ctx context.Context, req models.CreateProductRequest) (*models.Product, error) {
	p := models.Product{
		ID:          uuid.NewString(),
		Name:        req.Name,
		Price:       req.Price,
		Description: req.Description,
		Size:        req.Size,
		Category:    req.Category,
		Stock:       req.Stock,
		ImageURL:    req.ImageURL,
		Images:      req.Images,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO products (id, name, price, description, size, category, stock, image_url, images, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Price, p.Description, p.Size, p.Category, p.Stock,
		p.ImageURL, p.Images,
		p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert product: %w", err)
	}
	return &p, nil
}

// Update modifies an existing product's fields.
func (r *Repository) Update(ctx context.Context, id string, req models.UpdateProductRequest) (*models.Product, error) {
	now := time.Now()
	_, err := r.db.ExecContext(ctx,
		`UPDATE products
		 SET name=?, price=?, description=?, size=?, category=?, stock=?, image_url=?, images=?, updated_at=?
		 WHERE id=?`,
		req.Name, req.Price, req.Description, req.Size, req.Category, req.Stock,
		req.ImageURL, req.Images,
		now, id,
	)
	if err != nil {
		return nil, fmt.Errorf("update product: %w", err)
	}
	return r.GetByID(ctx, id)
}

// Delete removes a product by ID.
func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM products WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete product: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ---- helpers ----

func scanProducts(rows *sql.Rows) ([]models.Product, error) {
	var products []models.Product
	for rows.Next() {
		var p models.Product
		if err := rows.Scan(
			&p.ID, &p.Name, &p.Price, &p.Description, &p.Size, &p.Category, &p.Stock,
			&p.ImageURL, &p.Images,
			&p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan product row: %w", err)
		}
		products = append(products, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return products, nil
}
