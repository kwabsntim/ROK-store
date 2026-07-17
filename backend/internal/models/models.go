package models

import (
	"database/sql"
	"time"
)

// User represents a registered customer or admin.
type User struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"` // never serialized to JSON
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Product represents a skate store product.
type Product struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Price       float64   `json:"price"`
	Description string    `json:"description,omitempty"`
	Size        string    `json:"size,omitempty"`
	Category    string    `json:"category"`
	Stock       int       `json:"stock"`
	ImageURL    string    `json:"image_url,omitempty"`   // primary Cloudinary URL
	Images      string    `json:"images,omitempty"`      // JSON-encoded array of secondary URLs
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CartItem represents a product in an authenticated user's DB cart.
type CartItem struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ProductID string    `json:"product_id"`
	Quantity  int       `json:"quantity"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Joined fields — populated by repository queries
	Product *Product `json:"product,omitempty"`
}

// Order represents a customer order (guest or authenticated).
type Order struct {
	ID               string         `json:"id"`
	UserID           sql.NullString `json:"user_id,omitempty"`
	GuestEmail       sql.NullString `json:"guest_email,omitempty"`
	ShippingAddress  string         `json:"shipping_address"`
	TotalAmount      float64        `json:"total_amount"`
	PaymentStatus    string         `json:"payment_status"`
	PaymentReference string         `json:"payment_reference"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`

	Items []OrderItem `json:"items,omitempty"`
}

// OrderItem represents a line item in an order, preserving purchase-time price.
type OrderItem struct {
	ID              string  `json:"id"`
	OrderID         string  `json:"order_id"`
	ProductID       string  `json:"product_id"`
	Quantity        int     `json:"quantity"`
	PriceAtPurchase float64 `json:"price_at_purchase"`
}

// IdempotencyKey tracks checkout/payment requests to prevent double-charges.
type IdempotencyKey struct {
	Key          string         `json:"key"`
	UserID       sql.NullString `json:"user_id,omitempty"`
	GuestEmail   sql.NullString `json:"guest_email,omitempty"`
	RequestPath  string         `json:"request_path"`
	ResponseCode sql.NullInt32  `json:"response_code,omitempty"`
	ResponseBody sql.NullString `json:"response_body,omitempty"`
	Status       string         `json:"status"`
	CreatedAt    time.Time      `json:"created_at"`
	ExpiresAt    time.Time      `json:"expires_at"`
}

// ---- Request / Response DTOs ----

// RegisterRequest is the payload for POST /api/auth/register.
type RegisterRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginRequest is the payload for POST /api/auth/login.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// AuthResponse is returned after a successful register or login.
type AuthResponse struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}

// CreateProductRequest is the payload for POST /api/admin/products.
type CreateProductRequest struct {
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Description string  `json:"description"`
	Size        string  `json:"size"`
	Category    string  `json:"category"`
	Stock       int     `json:"stock"`
	ImageURL    string  `json:"image_url"`
	Images      string  `json:"images"` // JSON-encoded array of secondary URLs
}

// UpdateProductRequest is the payload for PUT /api/admin/products/:id.
type UpdateProductRequest struct {
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Description string  `json:"description"`
	Size        string  `json:"size"`
	Category    string  `json:"category"`
	Stock       int     `json:"stock"`
	ImageURL    string  `json:"image_url"`
	Images      string  `json:"images"` // JSON-encoded array of secondary URLs
}

// AddCartItemRequest is the payload for POST /api/cart.
type AddCartItemRequest struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

// UpdateCartItemRequest is the payload for PUT /api/cart/:product_id.
type UpdateCartItemRequest struct {
	Quantity int `json:"quantity"`
}

// GuestCartItem represents a single item in a guest checkout payload.
type GuestCartItem struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

// CheckoutRequest is the payload for POST /api/checkout.
// For guests: Email and CartItems must be provided.
// For authenticated users: only ShippingAddress is required (cart loaded from DB).
type CheckoutRequest struct {
	Email           string          `json:"email,omitempty"`
	ShippingAddress string          `json:"shipping_address"`
	CartItems       []GuestCartItem `json:"cart_items,omitempty"`
}

// CheckoutResponse is returned after a successful checkout initialization.
type CheckoutResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	Reference        string `json:"reference"`
	OrderID          string `json:"order_id"`
}

// CartResponse wraps cart items and the computed total for GET /api/cart.
type CartResponse struct {
	Items      []CartItem `json:"items"`
	TotalPrice float64    `json:"total_price"`
}

// PaginatedProductsResponse wraps product listings with pagination metadata.
type PaginatedProductsResponse struct {
	Products []Product `json:"products"`
	Total    int       `json:"total"`
	Page     int       `json:"page"`
	PageSize int       `json:"page_size"`
}
